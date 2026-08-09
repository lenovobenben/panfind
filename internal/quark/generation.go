package quark

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/lenovobenben/panfind/internal/namespace"
)

const (
	ProviderID      namespace.ProviderID = "quark-remote"
	syntheticRootID int64                = math.MinInt64
	rootRemoteID                         = "0"
)

var errNoPublishedSnapshot = errors.New("no published Quark snapshot")

func (s *store) stagingGeneration(ctx context.Context, accountID namespace.AccountID) (int64, bool, error) {
	if accountID == "" {
		return 0, false, errors.New("Quark account ID is empty")
	}
	var count int
	var runID int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(MAX(run_id), 0)
		FROM sync_runs
		WHERE account_id = ? AND state = 'staging'
	`, accountID).Scan(&count, &runID); err != nil {
		return 0, false, fmt.Errorf("read Quark staging generation: %w", err)
	}
	if count > 1 {
		return 0, false, fmt.Errorf("Quark account %q has %d staging generations", accountID, count)
	}
	return runID, count == 1, nil
}

func (s *store) beginGeneration(ctx context.Context, accountID namespace.AccountID) (int64, error) {
	if accountID == "" {
		return 0, errors.New("Quark account ID is empty")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin Quark generation transaction: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `
		INSERT INTO sync_runs(account_id, state, started_at, last_attempt_at)
		VALUES (?, 'staging', ?, ?)
	`, accountID, formatTime(&now), formatTime(&now))
	if err != nil {
		return 0, fmt.Errorf("create Quark staging generation: %w", err)
	}
	runID, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read Quark staging generation ID: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO nodes(run_id, local_id, parent_id, name, kind, size)
		VALUES (?, ?, 0, '/', ?, '0')
	`, runID, syntheticRootID, namespace.NodeKindDirectory); err != nil {
		return 0, fmt.Errorf("create Quark generation root: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO crawl_queue(run_id, local_id, remote_id, next_page, state)
		VALUES (?, ?, ?, 1, 'pending')
	`, runID, syntheticRootID, rootRemoteID); err != nil {
		return 0, fmt.Errorf("queue Quark generation root: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit Quark staging generation: %w", err)
	}
	return runID, nil
}

func (s *store) publishGeneration(ctx context.Context, runID int64) (*namespace.Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin Quark publish transaction: %w", err)
	}
	defer tx.Rollback()

	accountID, err := stagingAccount(ctx, tx, runID)
	if err != nil {
		return nil, err
	}
	if err := requireCompletedCrawl(ctx, tx, runID); err != nil {
		return nil, err
	}
	nodes, err := readGeneration(ctx, tx, runID, accountID)
	if err != nil {
		return nil, err
	}
	root := namespace.NodeKey{Provider: ProviderID, Account: accountID, ID: syntheticRootID}
	snapshot, err := namespace.NewSnapshot(uint64(runID), root, nodes)
	if err != nil {
		return nil, fmt.Errorf("validate Quark staging generation %d: %w", runID, err)
	}

	completedAt := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
		UPDATE sync_runs
		SET state = 'complete', completed_at = ?, last_progress_at = ?, last_error = NULL
		WHERE run_id = ?
	`, formatTime(&completedAt), formatTime(&completedAt), runID); err != nil {
		return nil, fmt.Errorf("complete Quark generation %d: %w", runID, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO published_generations(account_id, run_id)
		VALUES (?, ?)
		ON CONFLICT(account_id) DO UPDATE SET run_id = excluded.run_id
	`, accountID, runID); err != nil {
		return nil, fmt.Errorf("publish Quark generation %d: %w", runID, err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit Quark generation %d: %w", runID, err)
	}
	return snapshot, nil
}

func (s *store) loadPublishedSnapshot(ctx context.Context, accountID namespace.AccountID, generation uint64) (*namespace.Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin Quark snapshot read: %w", err)
	}
	defer tx.Rollback()

	var runID int64
	if err := tx.QueryRowContext(ctx, `
		SELECT published_generations.run_id
		FROM published_generations
		JOIN sync_runs ON sync_runs.run_id = published_generations.run_id
		WHERE published_generations.account_id = ?
		  AND sync_runs.state = 'complete'
	`, accountID).Scan(&runID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w for account %q", errNoPublishedSnapshot, accountID)
		}
		return nil, fmt.Errorf("read published Quark generation: %w", err)
	}
	nodes, err := readGeneration(ctx, tx, runID, accountID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit Quark snapshot read: %w", err)
	}

	root := namespace.NodeKey{Provider: ProviderID, Account: accountID, ID: syntheticRootID}
	snapshot, err := namespace.NewSnapshot(generation, root, nodes)
	if err != nil {
		return nil, fmt.Errorf("load published Quark generation %d: %w", runID, err)
	}
	return snapshot, nil
}

func stagingAccount(ctx context.Context, tx *sql.Tx, runID int64) (namespace.AccountID, error) {
	var accountID namespace.AccountID
	var state string
	if err := tx.QueryRowContext(ctx, `
		SELECT account_id, state
		FROM sync_runs
		WHERE run_id = ?
	`, runID).Scan(&accountID, &state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("Quark generation %d does not exist", runID)
		}
		return "", fmt.Errorf("read Quark generation %d: %w", runID, err)
	}
	if state != "staging" {
		return "", fmt.Errorf("Quark generation %d is not staging", runID)
	}
	return accountID, nil
}

func validateStagedNode(accountID namespace.AccountID, node namespace.Node) error {
	if node.Key.Provider != ProviderID || node.Key.Account != accountID {
		return fmt.Errorf("Quark node key is outside generation scope: %+v", node.Key)
	}
	if node.Parent.Provider != ProviderID || node.Parent.Account != accountID {
		return fmt.Errorf("parent of Quark node %+v is outside generation scope: %+v", node.Key, node.Parent)
	}
	if node.Key.ID <= 0 {
		return fmt.Errorf("Quark node ID must be positive: %d", node.Key.ID)
	}
	if node.Kind != namespace.NodeKindFile && node.Kind != namespace.NodeKindDirectory {
		return fmt.Errorf("Quark node %d has invalid kind %q", node.Key.ID, node.Kind)
	}
	if node.Name == "" || node.Name == "." || node.Name == ".." || strings.Contains(node.Name, "/") || strings.ContainsRune(node.Name, '\x00') {
		return fmt.Errorf("Quark node %d has invalid name %q", node.Key.ID, node.Name)
	}
	if node.Hash != nil || node.AddedAt != nil {
		return fmt.Errorf("Quark node %d contains unsupported metadata", node.Key.ID)
	}
	return nil
}

func readGeneration(ctx context.Context, tx *sql.Tx, runID int64, accountID namespace.AccountID) ([]namespace.Node, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT local_id, parent_id, name, kind, size,
		       modified_at, created_at, first_seen_at, category
		FROM nodes
		WHERE run_id = ?
		ORDER BY local_id
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("read Quark generation %d: %w", runID, err)
	}
	defer rows.Close()

	nodes := make([]namespace.Node, 0)
	for rows.Next() {
		var node namespace.Node
		var size string
		var modifiedAt sql.NullString
		var createdAt sql.NullString
		var firstSeenAt sql.NullString
		var category sql.NullInt64
		if err := rows.Scan(
			&node.Key.ID,
			&node.Parent.ID,
			&node.Name,
			&node.Kind,
			&size,
			&modifiedAt,
			&createdAt,
			&firstSeenAt,
			&category,
		); err != nil {
			return nil, fmt.Errorf("scan Quark generation %d node: %w", runID, err)
		}
		node.Key.Provider = ProviderID
		node.Key.Account = accountID
		node.Parent.Provider = ProviderID
		node.Parent.Account = accountID
		node.Size, err = strconv.ParseUint(size, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse Quark node %d size: %w", node.Key.ID, err)
		}
		if node.ModifiedAt, err = parseTime(modifiedAt); err != nil {
			return nil, fmt.Errorf("parse Quark node %d modified time: %w", node.Key.ID, err)
		}
		if node.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, fmt.Errorf("parse Quark node %d created time: %w", node.Key.ID, err)
		}
		if node.FirstSeenAt, err = parseTime(firstSeenAt); err != nil {
			return nil, fmt.Errorf("parse Quark node %d first seen time: %w", node.Key.ID, err)
		}
		if category.Valid {
			if category.Int64 < math.MinInt32 || category.Int64 > math.MaxInt32 {
				return nil, fmt.Errorf("Quark node %d category is outside int32 range", node.Key.ID)
			}
			value := int32(category.Int64)
			node.Category = &value
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Quark generation %d nodes: %w", runID, err)
	}
	return nodes, nil
}

func formatTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func parseTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
