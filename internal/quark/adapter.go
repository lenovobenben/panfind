package quark

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/lenovobenben/panfind/internal/namespace"
	"github.com/lenovobenben/panfind/internal/provider"
)

const metadataDatabaseName = "metadata.db"

type Adapter struct {
	databasePath  string
	authorization authorizationProvider
	pageSize      int
}

// New creates a Quark adapter backed by PanFind's private metadata cache.
func New() (*Adapter, error) {
	cacheDirectory, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("locate user cache directory: %w", err)
	}
	authorization, err := newDesktopAuthorizationProvider(&http.Client{Timeout: 30 * time.Second})
	if err != nil {
		return nil, err
	}
	return newAdapterAt(filepath.Join(cacheDirectory, "PanFind", "quark", metadataDatabaseName), authorization), nil
}

func newAdapterAt(databasePath string, authorization authorizationProvider) *Adapter {
	return &Adapter{databasePath: databasePath, authorization: authorization, pageSize: defaultPageSize}
}

func (adapter *Adapter) ID() namespace.ProviderID {
	return ProviderID
}

func (adapter *Adapter) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Size:       true,
		ModifiedAt: true,
		CreatedAt:  true,
		StableID:   true,
	}
}

func (adapter *Adapter) DiscoverAccounts(ctx context.Context) ([]provider.Account, error) {
	if adapter == nil || adapter.databasePath == "" {
		return nil, errors.New("Quark adapter database path is empty")
	}
	info, err := os.Stat(adapter.databasePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("inspect Quark metadata database: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("Quark metadata database is not a regular file")
	}
	store, err := openStore(adapter.databasePath)
	if err != nil {
		return nil, err
	}
	defer store.close()
	accountIDs, err := store.knownAccounts(ctx)
	if err != nil {
		return nil, err
	}
	accounts := make([]provider.Account, len(accountIDs))
	for index, accountID := range accountIDs {
		accounts[index] = provider.Account{
			Provider:     ProviderID,
			ID:           accountID,
			DisplayName:  "Quark Drive",
			DatabasePath: adapter.databasePath,
		}
	}
	return accounts, nil
}

// RefreshStatus reports the last published snapshot and any recoverable scan
// without contacting Quark.
func (adapter *Adapter) RefreshStatus(ctx context.Context, account provider.Account) (RefreshStatus, error) {
	if err := adapter.validateAccount(account); err != nil {
		return RefreshStatus{}, err
	}
	info, err := os.Stat(adapter.databasePath)
	if err != nil {
		if os.IsNotExist(err) {
			return RefreshStatus{State: RefreshStateEmpty}, nil
		}
		return RefreshStatus{}, fmt.Errorf("inspect Quark metadata database: %w", err)
	}
	if !info.Mode().IsRegular() {
		return RefreshStatus{}, errors.New("Quark metadata database is not a regular file")
	}
	store, err := openStore(adapter.databasePath)
	if err != nil {
		return RefreshStatus{}, err
	}
	defer store.close()
	return store.refreshStatus(ctx, account.ID)
}

func (adapter *Adapter) LoadSnapshot(ctx context.Context, account provider.Account, generation uint64) (*namespace.Snapshot, error) {
	if err := adapter.validateAccount(account); err != nil {
		return nil, err
	}
	info, err := os.Stat(adapter.databasePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w for account %q", errNoPublishedSnapshot, account.ID)
		}
		return nil, fmt.Errorf("inspect Quark metadata database: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("Quark metadata database is not a regular file")
	}
	store, err := openStore(adapter.databasePath)
	if err != nil {
		return nil, err
	}
	defer store.close()
	return store.loadPublishedSnapshot(ctx, account.ID, generation)
}

func (adapter *Adapter) validateAccount(account provider.Account) error {
	if adapter == nil || adapter.databasePath == "" {
		return errors.New("Quark adapter database path is empty")
	}
	if account.Provider != ProviderID {
		return fmt.Errorf("account provider %q does not match adapter %q", account.Provider, ProviderID)
	}
	if account.ID == "" {
		return errors.New("Quark account ID is empty")
	}
	if filepath.Clean(account.DatabasePath) != filepath.Clean(adapter.databasePath) {
		return errors.New("Quark account database path does not match adapter")
	}
	return nil
}

// Refresh authenticates through the running desktop client and replaces the
// current account's snapshot only after a complete remote scan succeeds.
func (adapter *Adapter) Refresh(ctx context.Context, observe func(AuthorizationNotice)) (*namespace.Snapshot, error) {
	if adapter == nil || adapter.databasePath == "" {
		return nil, errors.New("Quark adapter database path is empty")
	}
	if adapter.authorization == nil {
		return nil, errors.New("Quark adapter authorization provider is nil")
	}
	if err := os.MkdirAll(filepath.Dir(adapter.databasePath), 0o700); err != nil {
		return nil, fmt.Errorf("create Quark metadata directory: %w", err)
	}
	store, err := openStore(adapter.databasePath)
	if err != nil {
		return nil, err
	}
	defer store.close()
	runner, err := newRefreshRunner(store, adapter.authorization, adapter.pageSize)
	if err != nil {
		return nil, err
	}
	return runner.run(ctx, observe)
}

func (s *store) knownAccounts(ctx context.Context) ([]namespace.AccountID, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT account_id
		FROM published_generations
		UNION
		SELECT account_id
		FROM sync_runs
		WHERE state = 'staging'
		ORDER BY account_id
	`)
	if err != nil {
		return nil, fmt.Errorf("read known Quark accounts: %w", err)
	}
	defer rows.Close()
	accounts := make([]namespace.AccountID, 0)
	for rows.Next() {
		var accountID namespace.AccountID
		if err := rows.Scan(&accountID); err != nil {
			return nil, fmt.Errorf("scan known Quark account: %w", err)
		}
		accounts = append(accounts, accountID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate known Quark accounts: %w", err)
	}
	return accounts, nil
}

var _ provider.Adapter = (*Adapter)(nil)
