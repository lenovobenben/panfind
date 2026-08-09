package quark

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestStoreKeepsRemoteIDsStable(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "metadata.db")
	store, err := openStore(databasePath)
	if err != nil {
		t.Fatalf("openStore() error: %v", err)
	}

	ids, err := store.resolveRemoteIDs(context.Background(), "account-1", []string{"file-a", "file-b", "file-a"})
	if err != nil {
		store.close()
		t.Fatalf("resolveRemoteIDs() error: %v", err)
	}
	if ids[0] <= 0 || ids[1] <= 0 {
		store.close()
		t.Fatalf("resolveRemoteIDs() = %v, want positive IDs", ids)
	}
	if ids[0] != ids[2] {
		store.close()
		t.Fatalf("duplicate remote ID mapped to different IDs: %v", ids)
	}
	if ids[0] == ids[1] {
		store.close()
		t.Fatalf("different remote IDs mapped to the same ID: %v", ids)
	}
	if err := store.close(); err != nil {
		t.Fatalf("close() error: %v", err)
	}

	reopened, err := openStore(databasePath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.close()

	reopenedIDs, err := reopened.resolveRemoteIDs(context.Background(), "account-1", []string{"file-b", "file-a"})
	if err != nil {
		t.Fatalf("resolveRemoteIDs() after reopen error: %v", err)
	}
	if reopenedIDs[0] != ids[1] || reopenedIDs[1] != ids[0] {
		t.Fatalf("IDs changed after reopen: before=%v after=%v", ids, reopenedIDs)
	}
}

func TestStoreScopesRemoteIDsToAccount(t *testing.T) {
	store, err := openStore(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatalf("openStore() error: %v", err)
	}
	defer store.close()

	first, err := store.resolveRemoteIDs(context.Background(), "account-1", []string{"same-remote-id"})
	if err != nil {
		t.Fatalf("resolve account-1 ID: %v", err)
	}
	second, err := store.resolveRemoteIDs(context.Background(), "account-2", []string{"same-remote-id"})
	if err != nil {
		t.Fatalf("resolve account-2 ID: %v", err)
	}
	if first[0] == second[0] {
		t.Fatalf("remote ID was shared across accounts: %d", first[0])
	}
}

func TestStoreRejectsInvalidBatchWithoutWriting(t *testing.T) {
	store, err := openStore(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatalf("openStore() error: %v", err)
	}
	defer store.close()

	if _, err := store.resolveRemoteIDs(context.Background(), "account-1", []string{"file-a", ""}); err == nil {
		t.Fatal("resolveRemoteIDs() accepted an empty remote ID")
	}

	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM remote_ids").Scan(&count); err != nil {
		t.Fatalf("count remote IDs: %v", err)
	}
	if count != 0 {
		t.Fatalf("invalid batch wrote %d remote IDs", count)
	}
}

func TestOpenStoreRejectsNewerSchema(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "metadata.db")
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 5"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := openStore(databasePath)
	if store != nil {
		store.close()
		t.Fatal("openStore() returned a store for a newer schema")
	}
	if !errors.Is(err, errUnsupportedStoreSchema) {
		t.Fatalf("openStore() error = %v, want errUnsupportedStoreSchema", err)
	}
}

func TestOpenStoreMigratesStableIDSchema(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "metadata.db")
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		CREATE TABLE remote_ids(
			local_id INTEGER PRIMARY KEY AUTOINCREMENT,
			account_id TEXT NOT NULL,
			remote_id TEXT NOT NULL,
			UNIQUE(account_id, remote_id)
		);
		INSERT INTO remote_ids(account_id, remote_id) VALUES ('account-1', 'existing');
		PRAGMA user_version = 1;
	`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := openStore(databasePath)
	if err != nil {
		t.Fatalf("openStore() migration error: %v", err)
	}
	defer store.close()
	ids, err := store.resolveRemoteIDs(context.Background(), "account-1", []string{"existing"})
	if err != nil {
		t.Fatalf("resolveRemoteIDs() after migration: %v", err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("stable ID changed during migration: %v", ids)
	}
	if _, err := store.beginGeneration(context.Background(), "account-1"); err != nil {
		t.Fatalf("beginGeneration() after migration: %v", err)
	}
}

func TestOpenStoreMigratesRefreshStatusSchema(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "metadata.db")
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		CREATE TABLE sync_runs(
			run_id INTEGER PRIMARY KEY AUTOINCREMENT,
			account_id TEXT NOT NULL,
			state TEXT NOT NULL CHECK(state IN ('staging', 'complete'))
		);
		INSERT INTO sync_runs(account_id, state) VALUES ('account-1', 'complete');
		PRAGMA user_version = 3;
	`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := openStore(databasePath)
	if err != nil {
		t.Fatalf("openStore() migration error: %v", err)
	}
	defer store.close()
	var version int
	if err := store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read migrated schema version: %v", err)
	}
	if version != storeSchemaVersion {
		t.Fatalf("migrated schema version = %d, want %d", version, storeSchemaVersion)
	}
	rows, err := store.db.Query("PRAGMA table_info(sync_runs)")
	if err != nil {
		t.Fatalf("read sync_runs columns: %v", err)
	}
	defer rows.Close()
	wanted := map[string]bool{
		"started_at": false, "last_attempt_at": false, "last_progress_at": false,
		"completed_at": false, "last_error": false,
	}
	for rows.Next() {
		var position, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&position, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan sync_runs column: %v", err)
		}
		if _, exists := wanted[name]; exists {
			wanted[name] = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sync_runs columns: %v", err)
	}
	for name, found := range wanted {
		if !found {
			t.Errorf("migrated sync_runs is missing %s", name)
		}
	}
}
