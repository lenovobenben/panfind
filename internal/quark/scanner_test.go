package quark

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/lenovobenben/panfind/internal/namespace"
)

type directoryResponse struct {
	nodes []remoteNode
	err   error
}

type scriptedDirectoryClient struct {
	responses map[listDirectoryRequest]directoryResponse
	requests  []listDirectoryRequest
}

func (client *scriptedDirectoryClient) ListDirectory(_ context.Context, request listDirectoryRequest) ([]remoteNode, error) {
	client.requests = append(client.requests, request)
	response, exists := client.responses[request]
	if !exists {
		return nil, fmt.Errorf("unexpected directory request: %+v", request)
	}
	return response.nodes, response.err
}

func TestScannerBuildsRecursiveSnapshot(t *testing.T) {
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
	modifiedAt := time.Unix(1_700_000_001, 123).UTC()
	createdAt := time.Unix(1_600_000_001, 456).UTC()
	category := int32(1)
	client := &scriptedDirectoryClient{responses: map[listDirectoryRequest]directoryResponse{
		{DirectoryID: rootRemoteID, Page: 1, Size: 2}: {nodes: []remoteNode{
			{ID: "shows", ParentID: rootRemoteID, Name: "shows", Kind: namespace.NodeKindDirectory},
			{ID: "notes", ParentID: rootRemoteID, Name: "notes.txt", Kind: namespace.NodeKindFile, Size: 7},
		}},
		{DirectoryID: rootRemoteID, Page: 2, Size: 2}: {},
		{DirectoryID: "shows", Page: 1, Size: 2}: {nodes: []remoteNode{{
			ID:         "episode",
			ParentID:   "shows",
			Name:       "episode.mkv",
			Kind:       namespace.NodeKindFile,
			Size:       1024,
			ModifiedAt: &modifiedAt,
			CreatedAt:  &createdAt,
			Category:   &category,
		}}},
	}}
	scanner, err := newScanner(store, client, 2)
	if err != nil {
		t.Fatalf("newScanner() error: %v", err)
	}
	snapshot, err := scanner.run(ctx, runID)
	if err != nil {
		t.Fatalf("run() error: %v", err)
	}

	if len(client.requests) != 3 {
		t.Fatalf("directory requests = %+v", client.requests)
	}
	if client.requests[0].DirectoryID != rootRemoteID || client.requests[0].Page != 1 ||
		client.requests[1].DirectoryID != rootRemoteID || client.requests[1].Page != 2 ||
		client.requests[2].DirectoryID != "shows" || client.requests[2].Page != 1 {
		t.Fatalf("unexpected crawl order: %+v", client.requests)
	}
	stats := snapshot.DescendantStats()
	if stats.Nodes != 3 || stats.Files != 2 || stats.Directories != 1 {
		t.Fatalf("DescendantStats() = %+v", stats)
	}
	key, exists := snapshot.Lookup("/shows/episode.mkv")
	if !exists {
		t.Fatal("snapshot does not contain episode")
	}
	node, exists := snapshot.Node(key)
	if !exists || node.Size != 1024 || node.ModifiedAt == nil || !node.ModifiedAt.Equal(modifiedAt) ||
		node.CreatedAt == nil || !node.CreatedAt.Equal(createdAt) || node.Category == nil || *node.Category != category {
		t.Fatalf("unexpected episode node: %+v", node)
	}
}

func TestScannerStopsWhenRefreshHeartbeatIsLost(t *testing.T) {
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
	client := &scriptedDirectoryClient{responses: map[listDirectoryRequest]directoryResponse{}}
	scanner, err := newScanner(store, client, 1)
	if err != nil {
		t.Fatalf("newScanner() error: %v", err)
	}
	scanner.heartbeat = func(context.Context) error { return errQuarkRefreshLeaseLost }
	if _, err := scanner.run(ctx, runID); !errors.Is(err, errQuarkRefreshLeaseLost) {
		t.Fatalf("run() error = %v", err)
	}
	if len(client.requests) != 0 {
		t.Fatalf("scanner made requests after losing lease: %+v", client.requests)
	}
}

func TestScannerRenewsHeartbeatAfterRemoteRequestBeforeCommit(t *testing.T) {
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
	request := listDirectoryRequest{DirectoryID: rootRemoteID, Page: 1, Size: 1}
	client := &scriptedDirectoryClient{responses: map[listDirectoryRequest]directoryResponse{request: {}}}
	scanner, err := newScanner(store, client, 1)
	if err != nil {
		t.Fatalf("newScanner() error: %v", err)
	}
	heartbeats := 0
	scanner.heartbeat = func(context.Context) error {
		heartbeats++
		if heartbeats == 2 {
			return errQuarkRefreshLeaseLost
		}
		return nil
	}
	if _, err := scanner.run(ctx, runID); !errors.Is(err, errQuarkRefreshLeaseLost) {
		t.Fatalf("run() error = %v", err)
	}
	if len(client.requests) != 1 || heartbeats != 2 {
		t.Fatalf("requests = %+v, heartbeats = %d", client.requests, heartbeats)
	}
	page := mustNextCrawlPage(t, store, runID)
	if page.Number != 1 {
		t.Fatalf("page was committed after lease loss: %+v", page)
	}
}

func TestScannerResumesFailedPage(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "metadata.db")
	store, err := openStore(databasePath)
	if err != nil {
		t.Fatalf("openStore() error: %v", err)
	}
	runID, err := store.beginGeneration(ctx, "account-1")
	if err != nil {
		store.close()
		t.Fatalf("beginGeneration() error: %v", err)
	}

	firstClient := &scriptedDirectoryClient{responses: map[listDirectoryRequest]directoryResponse{
		{DirectoryID: rootRemoteID, Page: 1, Size: 1}: {nodes: []remoteNode{{
			ID: "file", ParentID: rootRemoteID, Name: "file.txt", Kind: namespace.NodeKindFile,
		}}},
		{DirectoryID: rootRemoteID, Page: 2, Size: 1}: {err: errors.New("temporary remote failure")},
	}}
	firstScanner, err := newScanner(store, firstClient, 1)
	if err != nil {
		store.close()
		t.Fatalf("newScanner() error: %v", err)
	}
	if _, err := firstScanner.run(ctx, runID); err == nil {
		store.close()
		t.Fatal("run() succeeded during remote failure")
	}
	if _, err := store.loadPublishedSnapshot(ctx, "account-1", 1); !errors.Is(err, errNoPublishedSnapshot) {
		store.close()
		t.Fatalf("loadPublishedSnapshot() error = %v, want errNoPublishedSnapshot", err)
	}
	page := mustNextCrawlPage(t, store, runID)
	if page.RemoteID != rootRemoteID || page.Number != 2 {
		store.close()
		t.Fatalf("checkpoint after failure = %+v", page)
	}
	if err := store.close(); err != nil {
		t.Fatalf("close() error: %v", err)
	}

	reopened, err := openStore(databasePath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.close()
	secondClient := &scriptedDirectoryClient{responses: map[listDirectoryRequest]directoryResponse{
		{DirectoryID: rootRemoteID, Page: 2, Size: 1}: {},
	}}
	secondScanner, err := newScanner(reopened, secondClient, 1)
	if err != nil {
		t.Fatalf("newScanner() after reopen error: %v", err)
	}
	snapshot, err := secondScanner.run(ctx, runID)
	if err != nil {
		t.Fatalf("resumed run() error: %v", err)
	}
	if len(secondClient.requests) != 1 || secondClient.requests[0].Page != 2 {
		t.Fatalf("resumed requests = %+v", secondClient.requests)
	}
	if _, exists := snapshot.Lookup("/file.txt"); !exists {
		t.Fatal("resumed snapshot lost the committed first page")
	}
}

func TestScannerRejectsRepeatedPage(t *testing.T) {
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

	repeated := remoteNode{
		ID: "same", ParentID: rootRemoteID, Name: "same.txt", Kind: namespace.NodeKindFile,
	}
	client := &scriptedDirectoryClient{responses: map[listDirectoryRequest]directoryResponse{
		{DirectoryID: rootRemoteID, Page: 1, Size: 1}: {nodes: []remoteNode{repeated}},
		{DirectoryID: rootRemoteID, Page: 2, Size: 1}: {nodes: []remoteNode{repeated}},
	}}
	scanner, err := newScanner(store, client, 1)
	if err != nil {
		t.Fatalf("newScanner() error: %v", err)
	}
	if _, err := scanner.run(ctx, runID); err == nil {
		t.Fatal("run() accepted a repeated page")
	}
	page := mustNextCrawlPage(t, store, runID)
	if page.RemoteID != rootRemoteID || page.Number != 2 {
		t.Fatalf("repeated page advanced checkpoint: %+v", page)
	}
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM nodes WHERE run_id = ?", runID).Scan(&count); err != nil {
		t.Fatalf("count staging nodes: %v", err)
	}
	if count != 2 {
		t.Fatalf("repeated page changed staging nodes: got %d, want root and first page", count)
	}
}

func TestScannerRejectsSuspiciousPageWithoutAdvancing(t *testing.T) {
	tests := []struct {
		name     string
		pageSize int
		nodes    []remoteNode
	}{
		{
			name:     "oversized",
			pageSize: 1,
			nodes: []remoteNode{
				{ID: "one", ParentID: rootRemoteID, Name: "one", Kind: namespace.NodeKindFile},
				{ID: "two", ParentID: rootRemoteID, Name: "two", Kind: namespace.NodeKindFile},
			},
		},
		{
			name:     "duplicate ID",
			pageSize: 2,
			nodes: []remoteNode{
				{ID: "same", ParentID: rootRemoteID, Name: "one", Kind: namespace.NodeKindFile},
				{ID: "same", ParentID: rootRemoteID, Name: "two", Kind: namespace.NodeKindFile},
			},
		},
		{
			name:     "unexpected parent",
			pageSize: 2,
			nodes: []remoteNode{
				{ID: "one", ParentID: "another-directory", Name: "one", Kind: namespace.NodeKindFile},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
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
			request := listDirectoryRequest{DirectoryID: rootRemoteID, Page: 1, Size: test.pageSize}
			client := &scriptedDirectoryClient{responses: map[listDirectoryRequest]directoryResponse{
				request: {nodes: test.nodes},
			}}
			scanner, err := newScanner(store, client, test.pageSize)
			if err != nil {
				t.Fatalf("newScanner() error: %v", err)
			}
			if _, err := scanner.run(ctx, runID); err == nil {
				t.Fatal("run() accepted a suspicious page")
			}
			page := mustNextCrawlPage(t, store, runID)
			if page.RemoteID != rootRemoteID || page.Number != 1 {
				t.Fatalf("suspicious page advanced checkpoint: %+v", page)
			}
			var count int
			if err := store.db.QueryRow("SELECT COUNT(*) FROM nodes WHERE run_id = ?", runID).Scan(&count); err != nil {
				t.Fatalf("count staging nodes: %v", err)
			}
			if count != 1 {
				t.Fatalf("suspicious page wrote %d nodes, want only the root", count)
			}
		})
	}
}
