package baidu

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lenovobenben/panfind/internal/namespace"
	"github.com/lenovobenben/panfind/internal/provider"
)

func TestLoadSnapshot(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "filecache.db")
	createTestDatabase(t, databasePath, `
		INSERT INTO file_meta
			(fid, parent_path, server_filename, file_size, md5, isdir, category, server_mtime, local_mtime)
		VALUES
			(10, '/',      'shows',       0,    '',     1, 6, 1700000000, 1700000000),
			(11, '/shows', 'episode.mkv', 1024, 'abcd', 0, 1, 1700000001, 1700000001)
	`)

	account := provider.Account{
		Provider:     ProviderID,
		ID:           "account-1",
		DatabasePath: databasePath,
	}
	snapshot, err := newAt(t.TempDir()).LoadSnapshot(context.Background(), account, 4)
	if err != nil {
		t.Fatalf("LoadSnapshot() error: %v", err)
	}

	if snapshot.Generation() != 4 {
		t.Fatalf("Generation() = %d, want 4", snapshot.Generation())
	}
	stats := snapshot.DescendantStats()
	if stats.Nodes != 2 || stats.Files != 1 || stats.Directories != 1 {
		t.Fatalf("DescendantStats() = %+v", stats)
	}

	fileKey := namespace.NodeKey{Provider: ProviderID, Account: "account-1", ID: 11}
	file, exists := snapshot.Node(fileKey)
	if !exists {
		t.Fatal("snapshot does not contain fid 11")
	}
	if file.Name != "episode.mkv" || file.Size != 1024 || file.Hash == nil || *file.Hash != "abcd" {
		t.Fatalf("unexpected file node: %+v", file)
	}
	if file.ModifiedAt == nil || file.ModifiedAt.Unix() != 1700000001 {
		t.Fatalf("unexpected modified time: %v", file.ModifiedAt)
	}
}

func TestLoadSnapshotRejectsUnsupportedSchema(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "filecache.db")
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE file_meta(fid INTEGER)"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	account := provider.Account{Provider: ProviderID, ID: "account-1", DatabasePath: databasePath}
	_, err = newAt(t.TempDir()).LoadSnapshot(context.Background(), account, 1)
	if !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("LoadSnapshot() error = %v, want ErrUnsupportedSchema", err)
	}
}

func createTestDatabase(t *testing.T, databasePath, seedSQL string) {
	t.Helper()
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`
		CREATE TABLE file_meta(
			id INTEGER PRIMARY KEY,
			fid INTEGER,
			parent_path TEXT,
			server_filename TEXT,
			file_size INTEGER,
			md5 TEXT,
			isdir BOOLEAN,
			category INTEGER,
			server_mtime INTEGER,
			local_mtime INTEGER
		);
	` + seedSQL)
	if err != nil {
		t.Fatalf("create test database: %v", err)
	}
}
