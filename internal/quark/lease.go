package quark

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/lenovobenben/panfind/internal/namespace"
)

const refreshLeaseStaleAfter = 10 * time.Minute

var (
	errQuarkRefreshInProgress = errors.New("another Quark refresh is already in progress for this account")
	errQuarkRefreshLeaseLost  = errors.New("Quark refresh lease was lost")
)

type refreshLease struct {
	store     *store
	accountID namespace.AccountID
	ownerID   string
}

func (s *store) acquireRefreshLease(ctx context.Context, accountID namespace.AccountID) (*refreshLease, error) {
	if accountID == "" {
		return nil, errors.New("Quark refresh lease account ID is empty")
	}
	ownerBytes := make([]byte, 16)
	if _, err := rand.Read(ownerBytes); err != nil {
		return nil, fmt.Errorf("create Quark refresh lease owner: %w", err)
	}
	ownerID := hex.EncodeToString(ownerBytes)
	now := time.Now().UTC()
	cutoff := now.Add(-refreshLeaseStaleAfter).UnixMilli()
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO refresh_leases(account_id, owner_id, heartbeat_unix_ms)
		VALUES (?, ?, ?)
		ON CONFLICT(account_id) DO UPDATE SET
			owner_id = excluded.owner_id,
			heartbeat_unix_ms = excluded.heartbeat_unix_ms
		WHERE refresh_leases.heartbeat_unix_ms <= ?
	`, accountID, ownerID, now.UnixMilli(), cutoff)
	if err != nil {
		return nil, fmt.Errorf("acquire Quark refresh lease: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("read Quark refresh lease acquisition: %w", err)
	}
	if changed != 1 {
		return nil, errQuarkRefreshInProgress
	}
	return &refreshLease{store: s, accountID: accountID, ownerID: ownerID}, nil
}

func (lease *refreshLease) renew(ctx context.Context) error {
	if lease == nil || lease.store == nil || lease.accountID == "" || lease.ownerID == "" {
		return errors.New("Quark refresh lease is invalid")
	}
	result, err := lease.store.db.ExecContext(ctx, `
		UPDATE refresh_leases
		SET heartbeat_unix_ms = ?
		WHERE account_id = ? AND owner_id = ?
	`, time.Now().UTC().UnixMilli(), lease.accountID, lease.ownerID)
	if err != nil {
		return fmt.Errorf("renew Quark refresh lease: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read Quark refresh lease renewal: %w", err)
	}
	if changed != 1 {
		return errQuarkRefreshLeaseLost
	}
	return nil
}

func (lease *refreshLease) release(ctx context.Context) error {
	if lease == nil || lease.store == nil || lease.accountID == "" || lease.ownerID == "" {
		return errors.New("Quark refresh lease is invalid")
	}
	if _, err := lease.store.db.ExecContext(ctx, `
		DELETE FROM refresh_leases
		WHERE account_id = ? AND owner_id = ?
	`, lease.accountID, lease.ownerID); err != nil {
		return fmt.Errorf("release Quark refresh lease: %w", err)
	}
	return nil
}
