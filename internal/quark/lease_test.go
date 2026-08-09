package quark

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestRefreshLeaseExcludesSameAccountAcrossStores(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "metadata.db")
	firstStore, err := openStore(databasePath)
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	defer firstStore.close()
	secondStore, err := openStore(databasePath)
	if err != nil {
		t.Fatalf("open second store: %v", err)
	}
	defer secondStore.close()

	firstLease, err := firstStore.acquireRefreshLease(ctx, "account-1")
	if err != nil {
		t.Fatalf("acquire first lease: %v", err)
	}
	if _, err := secondStore.acquireRefreshLease(ctx, "account-1"); !errors.Is(err, errQuarkRefreshInProgress) {
		t.Fatalf("acquire conflicting lease error = %v", err)
	}
	otherAccount, err := secondStore.acquireRefreshLease(ctx, "account-2")
	if err != nil {
		t.Fatalf("acquire other account lease: %v", err)
	}
	if err := otherAccount.release(ctx); err != nil {
		t.Fatalf("release other account lease: %v", err)
	}
	if err := firstLease.renew(ctx); err != nil {
		t.Fatalf("renew first lease: %v", err)
	}
	if err := firstLease.release(ctx); err != nil {
		t.Fatalf("release first lease: %v", err)
	}
	secondLease, err := secondStore.acquireRefreshLease(ctx, "account-1")
	if err != nil {
		t.Fatalf("acquire lease after release: %v", err)
	}
	if err := secondLease.release(ctx); err != nil {
		t.Fatalf("release second lease: %v", err)
	}
}

func TestRefreshLeaseAllowsStaleTakeoverWithoutOldOwnerRelease(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "metadata.db")
	firstStore, err := openStore(databasePath)
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	defer firstStore.close()
	secondStore, err := openStore(databasePath)
	if err != nil {
		t.Fatalf("open second store: %v", err)
	}
	defer secondStore.close()

	oldLease, err := firstStore.acquireRefreshLease(ctx, "account-1")
	if err != nil {
		t.Fatalf("acquire old lease: %v", err)
	}
	if _, err := firstStore.db.ExecContext(ctx, `
		UPDATE refresh_leases
		SET heartbeat_unix_ms = ?
		WHERE account_id = ?
	`, time.Now().Add(-refreshLeaseStaleAfter-time.Minute).UnixMilli(), "account-1"); err != nil {
		t.Fatalf("age lease heartbeat: %v", err)
	}
	newLease, err := secondStore.acquireRefreshLease(ctx, "account-1")
	if err != nil {
		t.Fatalf("take over stale lease: %v", err)
	}
	if err := oldLease.renew(ctx); !errors.Is(err, errQuarkRefreshLeaseLost) {
		t.Fatalf("old lease renewal error = %v", err)
	}
	if err := oldLease.release(ctx); err != nil {
		t.Fatalf("old lease release: %v", err)
	}
	if err := newLease.renew(ctx); err != nil {
		t.Fatalf("old owner release removed new lease: %v", err)
	}
	if err := newLease.release(ctx); err != nil {
		t.Fatalf("release new lease: %v", err)
	}
}
