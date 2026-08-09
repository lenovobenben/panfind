package quark

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lenovobenben/panfind/internal/namespace"
)

func TestStorePublishesOnlyCompleteGenerations(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "metadata.db")
	store, err := openStore(databasePath)
	if err != nil {
		t.Fatalf("openStore() error: %v", err)
	}

	ids, err := store.resolveRemoteIDs(ctx, "account-1", []string{"shows", "episode", "replacement-parent", "replacement"})
	if err != nil {
		store.close()
		t.Fatalf("resolveRemoteIDs() error: %v", err)
	}
	root := quarkKey("account-1", syntheticRootID)
	firstRun, err := store.beginGeneration(ctx, "account-1")
	if err != nil {
		store.close()
		t.Fatalf("beginGeneration() error: %v", err)
	}
	modifiedAt := time.Unix(1_700_000_001, 123).UTC()
	category := int32(1)
	rootPage := mustNextCrawlPage(t, store, firstRun)
	if err := store.commitCrawlPage(ctx, firstRun, rootPage, []crawledNode{
		{RemoteID: "shows", Node: namespace.Node{
			Key:    quarkKey("account-1", ids[0]),
			Parent: root,
			Name:   "shows",
			Kind:   namespace.NodeKindDirectory,
		}},
	}, true); err != nil {
		store.close()
		t.Fatalf("commit first root page: %v", err)
	}
	showsPage := mustNextCrawlPage(t, store, firstRun)
	if err := store.commitCrawlPage(ctx, firstRun, showsPage, []crawledNode{
		{RemoteID: "episode", Node: namespace.Node{
			Key:        quarkKey("account-1", ids[1]),
			Parent:     quarkKey("account-1", ids[0]),
			Name:       "episode.mkv",
			Kind:       namespace.NodeKindFile,
			Size:       1<<63 + 7,
			ModifiedAt: &modifiedAt,
			Category:   &category,
		}},
	}, true); err != nil {
		store.close()
		t.Fatalf("commit shows page: %v", err)
	}
	first, err := store.publishGeneration(ctx, firstRun)
	if err != nil {
		store.close()
		t.Fatalf("publish first generation: %v", err)
	}
	assertSnapshotFile(t, first, "/shows/episode.mkv", 1<<63+7, modifiedAt)

	secondRun, err := store.beginGeneration(ctx, "account-1")
	if err != nil {
		store.close()
		t.Fatalf("begin second generation: %v", err)
	}
	if _, err := store.publishGeneration(ctx, secondRun); err == nil {
		store.close()
		t.Fatal("publishGeneration() accepted an incomplete crawl queue")
	}

	published, err := store.loadPublishedSnapshot(ctx, "account-1", 99)
	if err != nil {
		store.close()
		t.Fatalf("load snapshot after failed publish: %v", err)
	}
	if published.Generation() != 99 {
		store.close()
		t.Fatalf("published Generation() = %d, want 99", published.Generation())
	}
	assertSnapshotFile(t, published, "/shows/episode.mkv", 1<<63+7, modifiedAt)
	if _, exists := published.Lookup("/replacement.pdf"); exists {
		store.close()
		t.Fatal("failed generation replaced the published snapshot")
	}

	if err := store.close(); err != nil {
		t.Fatalf("close() error: %v", err)
	}
	reopened, err := openStore(databasePath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.close()

	secondRootPage := mustNextCrawlPage(t, reopened, secondRun)
	if err := reopened.commitCrawlPage(ctx, secondRun, secondRootPage, []crawledNode{
		{RemoteID: "replacement-parent", Node: namespace.Node{
			Key:    quarkKey("account-1", ids[2]),
			Parent: root,
			Name:   "documents",
			Kind:   namespace.NodeKindDirectory,
		}},
	}, true); err != nil {
		t.Fatalf("resume root directory: %v", err)
	}
	documentsPage := mustNextCrawlPage(t, reopened, secondRun)
	if err := reopened.commitCrawlPage(ctx, secondRun, documentsPage, []crawledNode{
		{RemoteID: "replacement", Node: namespace.Node{
			Key:    quarkKey("account-1", ids[3]),
			Parent: quarkKey("account-1", ids[2]),
			Name:   "replacement.pdf",
			Kind:   namespace.NodeKindFile,
			Size:   42,
		}},
	}, true); err != nil {
		t.Fatalf("resume documents directory: %v", err)
	}
	if _, err := reopened.publishGeneration(ctx, secondRun); err != nil {
		t.Fatalf("publish resumed generation: %v", err)
	}
	latest, err := reopened.loadPublishedSnapshot(ctx, "account-1", 100)
	if err != nil {
		t.Fatalf("load resumed generation: %v", err)
	}
	if _, exists := latest.Lookup("/shows/episode.mkv"); exists {
		t.Fatal("old generation remained visible after replacement")
	}
	fileKey, exists := latest.Lookup("/documents/replacement.pdf")
	if !exists || fileKey.ID != ids[3] {
		t.Fatalf("replacement file lookup = (%+v, %t)", fileKey, exists)
	}
}

func TestStoreDoesNotExposeStagingGeneration(t *testing.T) {
	ctx := context.Background()
	store, err := openStore(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatalf("openStore() error: %v", err)
	}
	defer store.close()

	if _, err := store.beginGeneration(ctx, "account-1"); err != nil {
		t.Fatalf("beginGeneration() error: %v", err)
	}
	if _, err := store.loadPublishedSnapshot(ctx, "account-1", 1); !errors.Is(err, errNoPublishedSnapshot) {
		t.Fatalf("loadPublishedSnapshot() error = %v, want errNoPublishedSnapshot", err)
	}
}

func TestCommitCrawlPageRejectsAnotherAccount(t *testing.T) {
	ctx := context.Background()
	store, err := openStore(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatalf("openStore() error: %v", err)
	}
	defer store.close()

	runID, err := store.beginGeneration(ctx, "account-1")
	if err != nil {
		t.Fatalf("beginGeneration() error: %v", err)
	}
	page := mustNextCrawlPage(t, store, runID)
	err = store.commitCrawlPage(ctx, runID, page, []crawledNode{{
		RemoteID: "foreign",
		Node: namespace.Node{
			Key:    quarkKey("account-2", 1),
			Parent: quarkKey("account-2", syntheticRootID),
			Name:   "foreign",
			Kind:   namespace.NodeKindDirectory,
		},
	}}, true)
	if err == nil {
		t.Fatal("commitCrawlPage() accepted a node from another account")
	}
}

func quarkKey(accountID namespace.AccountID, id int64) namespace.NodeKey {
	return namespace.NodeKey{Provider: ProviderID, Account: accountID, ID: id}
}

func mustNextCrawlPage(t *testing.T, store *store, runID int64) crawlPage {
	t.Helper()
	page, exists, err := store.nextCrawlPage(context.Background(), runID)
	if err != nil {
		t.Fatalf("nextCrawlPage() error: %v", err)
	}
	if !exists {
		t.Fatal("nextCrawlPage() returned no page")
	}
	return page
}

func assertSnapshotFile(t *testing.T, snapshot *namespace.Snapshot, path string, size uint64, modifiedAt time.Time) {
	t.Helper()
	key, exists := snapshot.Lookup(path)
	if !exists {
		t.Fatalf("snapshot does not contain %q", path)
	}
	node, exists := snapshot.Node(key)
	if !exists {
		t.Fatalf("snapshot does not contain node %+v", key)
	}
	if node.Size != size || node.ModifiedAt == nil || !node.ModifiedAt.Equal(modifiedAt) {
		t.Fatalf("unexpected file node: %+v", node)
	}
}
