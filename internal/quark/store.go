// Package quark implements the Quark Drive remote snapshot provider.
package quark

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lenovobenben/panfind/internal/namespace"
	_ "modernc.org/sqlite"
)

const storeSchemaVersion = 5

var errUnsupportedStoreSchema = errors.New("unsupported Quark metadata store schema")

type store struct {
	db *sql.DB
}

func openStore(databasePath string) (*store, error) {
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return nil, fmt.Errorf("open Quark metadata store: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("configure Quark metadata store busy timeout: %w", err)
	}

	result := &store{db: db}
	if err := result.initialize(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return result, nil
}

func (s *store) close() error {
	return s.db.Close()
}

func (s *store) initialize(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Quark metadata store initialization: %w", err)
	}
	defer tx.Rollback()

	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read Quark metadata store schema version: %w", err)
	}
	if version > storeSchemaVersion {
		return fmt.Errorf("%w: found version %d, supports version %d", errUnsupportedStoreSchema, version, storeSchemaVersion)
	}
	if version < 1 {
		if _, err := tx.ExecContext(ctx, `
			CREATE TABLE remote_ids(
				local_id INTEGER PRIMARY KEY AUTOINCREMENT,
				account_id TEXT NOT NULL,
				remote_id TEXT NOT NULL,
				UNIQUE(account_id, remote_id)
			);
		`); err != nil {
			return fmt.Errorf("create Quark metadata store schema: %w", err)
		}
	}
	if version < 2 {
		if _, err := tx.ExecContext(ctx, `
			CREATE TABLE sync_runs(
				run_id INTEGER PRIMARY KEY AUTOINCREMENT,
				account_id TEXT NOT NULL,
				state TEXT NOT NULL CHECK(state IN ('staging', 'complete'))
			);
			CREATE TABLE nodes(
				run_id INTEGER NOT NULL,
				local_id INTEGER NOT NULL,
				parent_id INTEGER NOT NULL,
				name TEXT NOT NULL,
				kind INTEGER NOT NULL CHECK(kind IN (1, 2)),
				size TEXT NOT NULL,
				modified_at TEXT,
				created_at TEXT,
				first_seen_at TEXT,
				category INTEGER,
				PRIMARY KEY(run_id, local_id)
			);
			CREATE TABLE published_generations(
				account_id TEXT PRIMARY KEY,
				run_id INTEGER NOT NULL UNIQUE
			);
		`); err != nil {
			return fmt.Errorf("create Quark snapshot store schema: %w", err)
		}
	}
	if version < 3 {
		if _, err := tx.ExecContext(ctx, `
			CREATE TABLE crawl_queue(
				queue_id INTEGER PRIMARY KEY AUTOINCREMENT,
				run_id INTEGER NOT NULL,
				local_id INTEGER NOT NULL,
				remote_id TEXT NOT NULL,
				next_page INTEGER NOT NULL CHECK(next_page > 0),
				state TEXT NOT NULL CHECK(state IN ('pending', 'complete')),
				UNIQUE(run_id, local_id),
				UNIQUE(run_id, remote_id)
			);
		`); err != nil {
			return fmt.Errorf("create Quark crawl queue schema: %w", err)
		}
	}
	if version < 4 {
		if _, err := tx.ExecContext(ctx, `
			ALTER TABLE sync_runs ADD COLUMN started_at TEXT;
			ALTER TABLE sync_runs ADD COLUMN last_attempt_at TEXT;
			ALTER TABLE sync_runs ADD COLUMN last_progress_at TEXT;
			ALTER TABLE sync_runs ADD COLUMN completed_at TEXT;
			ALTER TABLE sync_runs ADD COLUMN last_error TEXT;
		`); err != nil {
			return fmt.Errorf("add Quark sync status schema: %w", err)
		}
	}
	if version < 5 {
		if _, err := tx.ExecContext(ctx, `
			CREATE TABLE refresh_leases(
				account_id TEXT PRIMARY KEY,
				owner_id TEXT NOT NULL,
				heartbeat_unix_ms INTEGER NOT NULL
			);
		`); err != nil {
			return fmt.Errorf("create Quark refresh lease schema: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, "PRAGMA user_version = 5"); err != nil {
		return fmt.Errorf("write Quark metadata store schema version: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Quark metadata store initialization: %w", err)
	}
	return nil
}

// resolveRemoteIDs returns stable internal IDs in the same order as remoteIDs.
// Remote identifiers are scoped to an account and remain allocated across scans.
func (s *store) resolveRemoteIDs(ctx context.Context, accountID namespace.AccountID, remoteIDs []string) ([]int64, error) {
	if accountID == "" {
		return nil, errors.New("Quark account ID is empty")
	}
	for _, remoteID := range remoteIDs {
		if remoteID == "" {
			return nil, errors.New("Quark remote ID is empty")
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin Quark remote ID transaction: %w", err)
	}
	defer tx.Rollback()

	result := make([]int64, len(remoteIDs))
	for index, remoteID := range remoteIDs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO remote_ids(account_id, remote_id)
			VALUES (?, ?)
			ON CONFLICT(account_id, remote_id) DO NOTHING
		`, accountID, remoteID); err != nil {
			return nil, fmt.Errorf("allocate internal ID for Quark remote ID: %w", err)
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT local_id
			FROM remote_ids
			WHERE account_id = ? AND remote_id = ?
		`, accountID, remoteID).Scan(&result[index]); err != nil {
			return nil, fmt.Errorf("read internal ID for Quark remote ID: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit Quark remote ID transaction: %w", err)
	}
	return result, nil
}
