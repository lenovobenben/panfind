package baidu

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverAccounts(t *testing.T) {
	usersDir := t.TempDir()
	for _, accountID := range []string{"account-b", "account-a"} {
		accountDir := filepath.Join(usersDir, accountID)
		if err := os.Mkdir(accountDir, 0o755); err != nil {
			t.Fatalf("Mkdir(%q): %v", accountDir, err)
		}
		if err := os.WriteFile(filepath.Join(accountDir, "filecache.db"), nil, 0o600); err != nil {
			t.Fatalf("WriteFile(%q): %v", accountDir, err)
		}
	}
	if err := os.Mkdir(filepath.Join(usersDir, "no-database"), 0o755); err != nil {
		t.Fatal(err)
	}

	accounts, err := newAt(usersDir).DiscoverAccounts(context.Background())
	if err != nil {
		t.Fatalf("DiscoverAccounts() error: %v", err)
	}
	if len(accounts) != 2 {
		t.Fatalf("DiscoverAccounts() returned %d accounts, want 2", len(accounts))
	}
	if accounts[0].ID != "account-a" || accounts[1].ID != "account-b" {
		t.Fatalf("accounts are not sorted: %+v", accounts)
	}
}

func TestDiscoverAccountsMissingDirectory(t *testing.T) {
	accounts, err := newAt(filepath.Join(t.TempDir(), "missing")).DiscoverAccounts(context.Background())
	if err != nil {
		t.Fatalf("DiscoverAccounts() error: %v", err)
	}
	if len(accounts) != 0 {
		t.Fatalf("DiscoverAccounts() returned %+v, want none", accounts)
	}
}
