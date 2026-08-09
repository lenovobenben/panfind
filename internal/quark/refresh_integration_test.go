package quark

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLiveDesktopRefresh(t *testing.T) {
	if os.Getenv("PANFIND_QUARK_LIVE_TEST") != "1" {
		t.Skip("set PANFIND_QUARK_LIVE_TEST=1 to run the read-only Quark desktop integration test")
	}

	store, err := openStore(filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatalf("openStore() error: %v", err)
	}
	defer store.close()
	authorization, err := newDesktopAuthorizationProvider(&http.Client{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("newDesktopAuthorizationProvider() error: %v", err)
	}
	runner, err := newRefreshRunner(store, authorization, defaultPageSize)
	if err != nil {
		t.Fatalf("newRefreshRunner() error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	snapshot, err := runner.run(ctx, func(notice AuthorizationNotice) {
		t.Logf("Quark desktop confirmation requested (prompt_opened=%t)", notice.PromptOpened)
	})
	if err != nil {
		t.Fatalf("live refresh error: %v", err)
	}
	stats := snapshot.DescendantStats()
	t.Logf("published read-only snapshot: generation=%d nodes=%d files=%d directories=%d",
		snapshot.Generation(), stats.Nodes, stats.Files, stats.Directories)
}
