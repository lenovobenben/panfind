package baidu

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lenovobenben/panfind/internal/namespace"
	"github.com/lenovobenben/panfind/internal/provider"
	"github.com/lenovobenben/panfind/internal/syncer"
)

type syncEvent struct {
	snapshot *namespace.Snapshot
	status   syncer.Status
}

func TestSQLiteChangesRefreshCompleteSnapshotsAndRecover(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "filecache.db")
	createTestDatabase(t, databasePath, `
		INSERT INTO file_meta
			(fid, parent_path, server_filename, file_size, md5, isdir, category, server_mtime, local_mtime)
		VALUES
			(10, '/',        'library', 0,   '',     1, 6, 1700000000, 1700000000),
			(11, '/library', 'old.txt', 100, 'old',  0, 1, 1700000001, 1700000001)
	`)

	writer, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatalf("enable WAL: %v", err)
	}

	account := provider.Account{
		Provider:     ProviderID,
		ID:           "integration-account",
		DatabasePath: databasePath,
	}
	session := syncer.New(newAt(t.TempDir()), account)
	events := make(chan syncEvent, 32)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- session.Run(ctx, syncer.WatchOptions{
			PollInterval: 10 * time.Millisecond,
			Debounce:     30 * time.Millisecond,
			MaxDelay:     100 * time.Millisecond,
		}, func(snapshot *namespace.Snapshot, status syncer.Status) error {
			events <- syncEvent{snapshot: snapshot, status: status}
			return nil
		})
	}()
	defer func() {
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("sync session stopped with %v", err)
		}
	}()

	initial := waitSyncEvent(t, events, func(event syncEvent) bool {
		return event.status.State == syncer.StateReady && event.status.Generation == 1
	})
	assertPath(t, initial.snapshot, "/library/old.txt", true)

	applyDatabaseChange(t, writer, `
		INSERT INTO file_meta
			(fid, parent_path, server_filename, file_size, md5, isdir, category, server_mtime, local_mtime)
		VALUES
			(12, '/', 'archive', 0, '', 1, 6, 1700000002, 1700000002),
			(13, '/', 'fresh.bin', 300, 'fresh', 0, 1, 1700000003, 1700000003);
		UPDATE file_meta
		SET parent_path = '/archive', server_filename = 'renamed.txt', file_size = 200, md5 = 'new'
		WHERE fid = 11;
		DELETE FROM file_meta WHERE fid = 10;
	`)

	changed := waitSyncEvent(t, events, func(event syncEvent) bool {
		return event.status.State == syncer.StateReady && event.status.Generation >= 2 &&
			hasPath(event.snapshot, "/archive/renamed.txt")
	})
	assertPath(t, changed.snapshot, "/library", false)
	assertPath(t, changed.snapshot, "/library/old.txt", false)
	assertPath(t, changed.snapshot, "/archive/renamed.txt", true)
	assertPath(t, changed.snapshot, "/fresh.bin", true)
	renamedKey, _ := changed.snapshot.Lookup("/archive/renamed.txt")
	renamed, _ := changed.snapshot.Node(renamedKey)
	if renamed.Size != 200 || renamed.Hash == nil || *renamed.Hash != "new" {
		t.Fatalf("renamed node was not updated: %+v", renamed)
	}
	stableSnapshot := changed.snapshot
	stableGeneration := changed.snapshot.Generation()

	applyDatabaseChange(t, writer, `
		INSERT INTO file_meta
			(fid, parent_path, server_filename, file_size, md5, isdir, category, server_mtime, local_mtime)
		VALUES
			(99, '/missing', 'broken.bin', 1, '', 0, 1, 1700000004, 1700000004)
	`)
	stale := waitSyncEvent(t, events, func(event syncEvent) bool {
		return event.status.State == syncer.StateStale
	})
	if stale.snapshot != stableSnapshot || session.Current() != stableSnapshot {
		t.Fatal("failed database refresh replaced the last complete snapshot")
	}
	if stale.status.Generation != stableGeneration || stale.status.LastError == "" {
		t.Fatalf("stale status = %+v", stale.status)
	}

	applyDatabaseChange(t, writer, `
		DELETE FROM file_meta WHERE fid = 99;
		INSERT INTO file_meta
			(fid, parent_path, server_filename, file_size, md5, isdir, category, server_mtime, local_mtime)
		VALUES
			(14, '/', 'recovered.bin', 400, 'recovered', 0, 1, 1700000005, 1700000005)
	`)
	recovered := waitSyncEvent(t, events, func(event syncEvent) bool {
		return event.status.State == syncer.StateReady && event.status.Generation > stableGeneration &&
			hasPath(event.snapshot, "/recovered.bin")
	})
	assertPath(t, recovered.snapshot, "/missing/broken.bin", false)
	assertPath(t, recovered.snapshot, "/recovered.bin", true)
}

func TestRefreshRetainsSnapshotDuringExclusiveDatabaseLock(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "filecache.db")
	createTestDatabase(t, databasePath, `
		INSERT INTO file_meta
			(fid, parent_path, server_filename, file_size, md5, isdir, category, server_mtime, local_mtime)
		VALUES
			(10, '/', 'locked.txt', 100, '', 0, 1, 1700000000, 1700000000)
	`)
	account := provider.Account{Provider: ProviderID, ID: "locked-account", DatabasePath: databasePath}
	session := syncer.New(newAt(t.TempDir()), account)
	if err := session.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	stable := session.Current()

	writer, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	connection, err := writer.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(context.Background(), "BEGIN EXCLUSIVE"); err != nil {
		t.Fatalf("acquire exclusive database lock: %v", err)
	}

	refreshErr := session.Refresh(context.Background())
	if refreshErr == nil {
		connection.ExecContext(context.Background(), "ROLLBACK")
		t.Fatal("refresh succeeded while the database was exclusively locked")
	}
	if session.Current() != stable || session.Status().State != syncer.StateStale {
		t.Fatal("exclusive lock discarded the last complete snapshot")
	}
	if _, err := connection.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatalf("release exclusive database lock: %v", err)
	}

	if err := session.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh after releasing lock: %v", err)
	}
	if session.Current().Generation() != 2 || session.Status().State != syncer.StateReady {
		t.Fatalf("session did not recover after lock: %+v", session.Status())
	}
}

func applyDatabaseChange(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(statement); err != nil {
		tx.Rollback()
		t.Fatalf("apply database change: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit database change: %v", err)
	}
}

func waitSyncEvent(t *testing.T, events <-chan syncEvent, matches func(syncEvent) bool) syncEvent {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if matches(event) {
				return event
			}
		case <-timer.C:
			t.Fatal("timed out waiting for synchronization event")
		}
	}
}

func assertPath(t *testing.T, snapshot *namespace.Snapshot, path string, want bool) {
	t.Helper()
	if got := hasPath(snapshot, path); got != want {
		t.Fatalf("snapshot lookup %q = %t, want %t", path, got, want)
	}
}

func hasPath(snapshot *namespace.Snapshot, path string) bool {
	_, exists := snapshot.Lookup(path)
	return exists
}
