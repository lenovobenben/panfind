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
	if _, err := db.Exec("PRAGMA user_version = 2"); err != nil {
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
