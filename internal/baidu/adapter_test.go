package baidu

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultUsersDir(t *testing.T) {
	configDir := filepath.Join("home", "config")
	tests := []struct {
		name string
		goos string
		want string
	}{
		{
			name: "Windows",
			goos: "windows",
			want: filepath.Join(configDir, "baidu", "BaiduNetdisk", "module", "BrowserEngine", "users"),
		},
		{
			name: "macOS",
			goos: "darwin",
			want: filepath.Join(configDir, "com.baidu.BaiduNetdisk-mac"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := defaultUsersDir(configDir, test.goos)
			if err != nil {
				t.Fatalf("defaultUsersDir() error: %v", err)
			}
			if got != test.want {
				t.Fatalf("defaultUsersDir() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDefaultUsersDirRejectsUnsupportedPlatform(t *testing.T) {
	if _, err := defaultUsersDir("config", "linux"); err == nil {
		t.Fatal("defaultUsersDir() accepted an unsupported platform")
	}
}

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
