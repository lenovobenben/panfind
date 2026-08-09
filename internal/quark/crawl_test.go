package quark

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lenovobenben/panfind/internal/namespace"
)

func TestCrawlQueuePersistsPageProgress(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "metadata.db")
	store, err := openStore(databasePath)
	if err != nil {
		t.Fatalf("openStore() error: %v", err)
	}

	ids, err := store.resolveRemoteIDs(ctx, "account-1", []string{"first-file", "directory", "second-file"})
	if err != nil {
		store.close()
		t.Fatalf("resolveRemoteIDs() error: %v", err)
	}
	runID, err := store.beginGeneration(ctx, "account-1")
	if err != nil {
		store.close()
		t.Fatalf("beginGeneration() error: %v", err)
	}
	root := quarkKey("account-1", syntheticRootID)
	firstPage := mustNextCrawlPage(t, store, runID)
	if firstPage.DirectoryID != syntheticRootID || firstPage.RemoteID != rootRemoteID || firstPage.Number != 1 {
		store.close()
		t.Fatalf("first crawl page = %+v", firstPage)
	}
	if err := store.commitCrawlPage(ctx, runID, firstPage, []crawledNode{{
		RemoteID: "first-file",
		Node: namespace.Node{
			Key:    quarkKey("account-1", ids[0]),
			Parent: root,
			Name:   "first.txt",
			Kind:   namespace.NodeKindFile,
		},
	}}, false); err != nil {
		store.close()
		t.Fatalf("commit first page: %v", err)
	}
	if err := store.commitCrawlPage(ctx, runID, firstPage, nil, false); err == nil {
		store.close()
		t.Fatal("commitCrawlPage() accepted a stale page checkpoint")
	}

	secondPage := mustNextCrawlPage(t, store, runID)
	if secondPage.DirectoryID != syntheticRootID || secondPage.Number != 2 {
		store.close()
		t.Fatalf("second crawl page = %+v", secondPage)
	}
	if err := store.close(); err != nil {
		t.Fatalf("close() error: %v", err)
	}

	reopened, err := openStore(databasePath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.close()
	resumedPage := mustNextCrawlPage(t, reopened, runID)
	if resumedPage != secondPage {
		t.Fatalf("crawl checkpoint changed after reopen: before=%+v after=%+v", secondPage, resumedPage)
	}
	if err := reopened.commitCrawlPage(ctx, runID, resumedPage, []crawledNode{
		{
			RemoteID: "directory",
			Node: namespace.Node{
				Key:    quarkKey("account-1", ids[1]),
				Parent: root,
				Name:   "documents",
				Kind:   namespace.NodeKindDirectory,
			},
		},
		{
			RemoteID: "second-file",
			Node: namespace.Node{
				Key:    quarkKey("account-1", ids[2]),
				Parent: root,
				Name:   "second.txt",
				Kind:   namespace.NodeKindFile,
			},
		},
	}, true); err != nil {
		t.Fatalf("commit resumed page: %v", err)
	}

	directoryPage := mustNextCrawlPage(t, reopened, runID)
	if directoryPage.DirectoryID != ids[1] || directoryPage.RemoteID != "directory" || directoryPage.Number != 1 {
		t.Fatalf("directory crawl page = %+v", directoryPage)
	}
	if err := reopened.commitCrawlPage(ctx, runID, directoryPage, nil, true); err != nil {
		t.Fatalf("complete empty directory: %v", err)
	}
	if page, exists, err := reopened.nextCrawlPage(ctx, runID); err != nil || exists {
		t.Fatalf("nextCrawlPage() after completion = (%+v, %t, %v)", page, exists, err)
	}
	if _, err := reopened.publishGeneration(ctx, runID); err != nil {
		t.Fatalf("publishGeneration() error: %v", err)
	}
}

func TestCommitCrawlPageIsAtomic(t *testing.T) {
	ctx := context.Background()
	store, err := openStore(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatalf("openStore() error: %v", err)
	}
	defer store.close()

	ids, err := store.resolveRemoteIDs(ctx, "account-1", []string{"valid"})
	if err != nil {
		t.Fatalf("resolveRemoteIDs() error: %v", err)
	}
	runID, err := store.beginGeneration(ctx, "account-1")
	if err != nil {
		t.Fatalf("beginGeneration() error: %v", err)
	}
	root := quarkKey("account-1", syntheticRootID)
	page := mustNextCrawlPage(t, store, runID)
	err = store.commitCrawlPage(ctx, runID, page, []crawledNode{
		{
			RemoteID: "valid",
			Node: namespace.Node{
				Key:    quarkKey("account-1", ids[0]),
				Parent: root,
				Name:   "valid.txt",
				Kind:   namespace.NodeKindFile,
			},
		},
		{
			RemoteID: "unresolved",
			Node: namespace.Node{
				Key:    quarkKey("account-1", ids[0]+1),
				Parent: root,
				Name:   "unresolved.txt",
				Kind:   namespace.NodeKindFile,
			},
		},
	}, true)
	if err == nil {
		t.Fatal("commitCrawlPage() accepted an unresolved remote ID")
	}

	retried := mustNextCrawlPage(t, store, runID)
	if retried != page {
		t.Fatalf("failed page advanced checkpoint: before=%+v after=%+v", page, retried)
	}
	var count int
	if err := store.db.QueryRow(`
		SELECT COUNT(*)
		FROM nodes
		WHERE run_id = ?
	`, runID).Scan(&count); err != nil {
		t.Fatalf("count staging nodes: %v", err)
	}
	if count != 1 {
		t.Fatalf("failed page left %d nodes, want only the root", count)
	}
}
