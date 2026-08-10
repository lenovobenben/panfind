// Package baidu implements the read-only Baidu Netdisk local database adapter.
package baidu

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/lenovobenben/panfind/internal/namespace"
	"github.com/lenovobenben/panfind/internal/provider"
)

const ProviderID namespace.ProviderID = "baidu-local"

type Adapter struct {
	usersDir string
}

// New creates an adapter for the current user's Baidu Netdisk desktop cache.
func New() (*Adapter, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("locate user config directory: %w", err)
	}
	usersDir, err := defaultUsersDir(configDir, runtime.GOOS)
	if err != nil {
		return nil, err
	}
	return newAt(usersDir), nil
}

func defaultUsersDir(configDir, goos string) (string, error) {
	switch goos {
	case "windows":
		return filepath.Join(configDir, "baidu", "BaiduNetdisk", "module", "BrowserEngine", "users"), nil
	case "darwin":
		return filepath.Join(configDir, "com.baidu.BaiduNetdisk-mac"), nil
	default:
		return "", fmt.Errorf("Baidu Netdisk desktop cache discovery is unsupported on %s", goos)
	}
}

func newAt(usersDir string) *Adapter {
	return &Adapter{usersDir: usersDir}
}

func (a *Adapter) ID() namespace.ProviderID {
	return ProviderID
}

// Capabilities describes fields verified in the local filecache.db schema.
func (a *Adapter) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Size:       true,
		ModifiedAt: true,
		Hash:       true,
		StableID:   true,
	}
}

func (a *Adapter) DiscoverAccounts(_ context.Context) ([]provider.Account, error) {
	entries, err := os.ReadDir(a.usersDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read Baidu account directory: %w", err)
	}

	accounts := make([]provider.Account, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		databasePath := filepath.Join(a.usersDir, entry.Name(), "filecache.db")
		info, err := os.Stat(databasePath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("inspect account %q database: %w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() {
			continue
		}

		accounts = append(accounts, provider.Account{
			Provider:     ProviderID,
			ID:           namespace.AccountID(entry.Name()),
			DisplayName:  entry.Name(),
			DatabasePath: databasePath,
		})
	}

	sort.Slice(accounts, func(i, j int) bool {
		return accounts[i].ID < accounts[j].ID
	})
	return accounts, nil
}

func (a *Adapter) LoadSnapshot(ctx context.Context, account provider.Account, generation uint64) (*namespace.Snapshot, error) {
	if account.Provider != ProviderID {
		return nil, fmt.Errorf("account provider %q does not match adapter %q", account.Provider, ProviderID)
	}
	return loadSnapshot(ctx, account, generation)
}

var _ provider.Adapter = (*Adapter)(nil)
