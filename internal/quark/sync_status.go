package quark

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lenovobenben/panfind/internal/namespace"
)

const maxRefreshErrorRunes = 2048

type RefreshState string

const (
	RefreshStateEmpty   RefreshState = "empty"
	RefreshStateReady   RefreshState = "ready"
	RefreshStateStaging RefreshState = "staging"
	RefreshStateFailed  RefreshState = "failed"
)

// RefreshStatus describes the published snapshot and any recoverable scan
// that is still being built for one Quark account.
type RefreshStatus struct {
	State                 RefreshState
	PublishedGeneration   int64
	SnapshotUpdatedAt     *time.Time
	StagingGeneration     int64
	StartedAt             *time.Time
	LastAttemptAt         *time.Time
	LastProgressAt        *time.Time
	LastError             string
	DirectoriesDiscovered int
	DirectoriesCompleted  int
	DirectoriesPending    int
	StagedNodes           int
}

func (s *store) markGenerationAttempt(ctx context.Context, runID int64) error {
	attemptedAt := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `
		UPDATE sync_runs
		SET last_attempt_at = ?
		WHERE run_id = ? AND state = 'staging'
	`, formatTime(&attemptedAt), runID)
	if err != nil {
		return fmt.Errorf("record Quark refresh attempt: %w", err)
	}
	return requireOneChangedRun(result, runID, "record refresh attempt")
}

func (s *store) recordGenerationFailure(ctx context.Context, runID int64, refreshErr error) error {
	if refreshErr == nil {
		return errors.New("Quark refresh failure is nil")
	}
	message := truncateRunes(refreshErr.Error(), maxRefreshErrorRunes)
	result, err := s.db.ExecContext(ctx, `
		UPDATE sync_runs
		SET last_error = ?
		WHERE run_id = ? AND state = 'staging'
	`, message, runID)
	if err != nil {
		return fmt.Errorf("record Quark refresh failure: %w", err)
	}
	return requireOneChangedRun(result, runID, "record refresh failure")
}

func requireOneChangedRun(result sql.Result, runID int64, action string) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read affected rows after %s: %w", action, err)
	}
	if changed != 1 {
		return fmt.Errorf("cannot %s for Quark staging generation %d", action, runID)
	}
	return nil
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 || len(value) == 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func (s *store) refreshStatus(ctx context.Context, accountID namespace.AccountID) (RefreshStatus, error) {
	if accountID == "" {
		return RefreshStatus{}, errors.New("Quark account ID is empty")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RefreshStatus{}, fmt.Errorf("begin Quark refresh status read: %w", err)
	}
	defer tx.Rollback()

	status := RefreshStatus{State: RefreshStateEmpty}
	var completedAt sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT published_generations.run_id, sync_runs.completed_at
		FROM published_generations
		JOIN sync_runs ON sync_runs.run_id = published_generations.run_id
		WHERE published_generations.account_id = ?
		  AND sync_runs.state = 'complete'
	`, accountID).Scan(&status.PublishedGeneration, &completedAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return RefreshStatus{}, fmt.Errorf("read published Quark refresh status: %w", err)
	}
	if err == nil {
		status.State = RefreshStateReady
		status.SnapshotUpdatedAt, err = parseTime(completedAt)
		if err != nil {
			return RefreshStatus{}, fmt.Errorf("parse Quark snapshot completion time: %w", err)
		}
	}

	var startedAt, lastAttemptAt, lastProgressAt, lastError sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT run_id, started_at, last_attempt_at, last_progress_at, last_error
		FROM sync_runs
		WHERE account_id = ? AND state = 'staging'
	`, accountID).Scan(
		&status.StagingGeneration,
		&startedAt,
		&lastAttemptAt,
		&lastProgressAt,
		&lastError,
	)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return RefreshStatus{}, fmt.Errorf("read staging Quark refresh status: %w", err)
	}
	if err == nil {
		status.State = RefreshStateStaging
		status.StartedAt, err = parseTime(startedAt)
		if err != nil {
			return RefreshStatus{}, fmt.Errorf("parse Quark refresh start time: %w", err)
		}
		status.LastAttemptAt, err = parseTime(lastAttemptAt)
		if err != nil {
			return RefreshStatus{}, fmt.Errorf("parse Quark refresh attempt time: %w", err)
		}
		status.LastProgressAt, err = parseTime(lastProgressAt)
		if err != nil {
			return RefreshStatus{}, fmt.Errorf("parse Quark refresh progress time: %w", err)
		}
		if lastError.Valid && strings.TrimSpace(lastError.String) != "" {
			status.State = RefreshStateFailed
			status.LastError = lastError.String
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*),
			       COALESCE(SUM(CASE WHEN state = 'complete' THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN state = 'pending' THEN 1 ELSE 0 END), 0)
			FROM crawl_queue
			WHERE run_id = ?
		`, status.StagingGeneration).Scan(
			&status.DirectoriesDiscovered,
			&status.DirectoriesCompleted,
			&status.DirectoriesPending,
		); err != nil {
			return RefreshStatus{}, fmt.Errorf("read Quark refresh directory progress: %w", err)
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT CASE WHEN COUNT(*) > 0 THEN COUNT(*) - 1 ELSE 0 END
			FROM nodes
			WHERE run_id = ?
		`, status.StagingGeneration).Scan(&status.StagedNodes); err != nil {
			return RefreshStatus{}, fmt.Errorf("read Quark staged node count: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return RefreshStatus{}, fmt.Errorf("commit Quark refresh status read: %w", err)
	}
	return status, nil
}
