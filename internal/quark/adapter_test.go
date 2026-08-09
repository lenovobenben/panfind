package quark

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lenovobenben/panfind/internal/namespace"
	"github.com/lenovobenben/panfind/internal/provider"
)

func TestAdapterRefreshDiscoverAndLoad(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "quark", "metadata.db")
	client := &scriptedDirectoryClient{responses: map[listDirectoryRequest]directoryResponse{
		{DirectoryID: rootRemoteID, Page: 1, Size: 2}: {nodes: []remoteNode{{
			ID: "document", ParentID: rootRemoteID, Name: "document.pdf", Kind: namespace.NodeKindFile, Size: 42,
		}}},
	}}
	session := &fakeRefreshSession{client: client}
	authorization := &fakeAuthorizationProvider{authorizations: []*fakePendingAuthorization{{
		id: "account-1", prompt: true, session: session,
	}}}
	adapter := newAdapterAt(databasePath, authorization)
	adapter.pageSize = 2

	var notice AuthorizationNotice
	snapshot, err := adapter.Refresh(ctx, func(value AuthorizationNotice) {
		notice = value
	})
	if err != nil {
		t.Fatalf("Refresh() error: %v", err)
	}
	if notice.AccountID != "account-1" || !notice.PromptOpened {
		t.Fatalf("authorization notice = %+v", notice)
	}
	if _, exists := snapshot.Lookup("/document.pdf"); !exists {
		t.Fatal("refreshed snapshot does not contain document")
	}

	accounts, err := adapter.DiscoverAccounts(ctx)
	if err != nil {
		t.Fatalf("DiscoverAccounts() error: %v", err)
	}
	if len(accounts) != 1 || accounts[0].Provider != ProviderID || accounts[0].ID != "account-1" ||
		accounts[0].DatabasePath != databasePath {
		t.Fatalf("discovered accounts = %+v", accounts)
	}
	loaded, err := adapter.LoadSnapshot(ctx, accounts[0], 9)
	if err != nil {
		t.Fatalf("LoadSnapshot() error: %v", err)
	}
	if loaded.Generation() != 9 {
		t.Fatalf("loaded generation = %d", loaded.Generation())
	}
	if _, exists := loaded.Lookup("/document.pdf"); !exists {
		t.Fatal("loaded snapshot does not contain document")
	}
}

func TestAdapterDiscoverAccountsDoesNotCreateDatabase(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "missing", "metadata.db")
	adapter := newAdapterAt(databasePath, nil)
	accounts, err := adapter.DiscoverAccounts(context.Background())
	if err != nil {
		t.Fatalf("DiscoverAccounts() error: %v", err)
	}
	if len(accounts) != 0 {
		t.Fatalf("DiscoverAccounts() = %+v", accounts)
	}
	if _, err := os.Stat(databasePath); !os.IsNotExist(err) {
		t.Fatalf("database was created during discovery: %v", err)
	}
}

func TestAdapterCapabilities(t *testing.T) {
	adapter := newAdapterAt(filepath.Join(t.TempDir(), "metadata.db"), nil)
	capabilities := adapter.Capabilities()
	if !capabilities.Size || !capabilities.ModifiedAt || !capabilities.CreatedAt || !capabilities.StableID ||
		capabilities.Hash || capabilities.AddedAt || capabilities.IncrementalHint {
		t.Fatalf("Capabilities() = %+v", capabilities)
	}
}

func TestAdapterRejectsForeignAccount(t *testing.T) {
	adapter := newAdapterAt(filepath.Join(t.TempDir(), "metadata.db"), nil)
	_, err := adapter.LoadSnapshot(context.Background(), provider.Account{
		Provider: "baidu-local", ID: "account-1", DatabasePath: adapter.databasePath,
	}, 1)
	if err == nil {
		t.Fatal("LoadSnapshot() accepted a foreign account")
	}
}

func TestAdapterLoadDoesNotCreateMissingDatabase(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "missing", "metadata.db")
	adapter := newAdapterAt(databasePath, nil)
	_, err := adapter.LoadSnapshot(context.Background(), provider.Account{
		Provider: ProviderID, ID: "account-1", DatabasePath: databasePath,
	}, 1)
	if !errors.Is(err, errNoPublishedSnapshot) {
		t.Fatalf("LoadSnapshot() error = %v", err)
	}
	if _, err := os.Stat(databasePath); !os.IsNotExist(err) {
		t.Fatalf("database was created during load: %v", err)
	}
}
