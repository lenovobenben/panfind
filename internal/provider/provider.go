// Package provider defines the boundary between cloud-drive adapters and PanFind.
package provider

import (
	"context"

	"github.com/lenovobenben/panfind/internal/namespace"
)

type Capabilities struct {
	Size            bool `json:"size"`
	ModifiedAt      bool `json:"modified_at"`
	CreatedAt       bool `json:"created_at"`
	AddedAt         bool `json:"added_at"`
	Hash            bool `json:"hash"`
	StableID        bool `json:"stable_id"`
	IncrementalHint bool `json:"incremental_hint"`
}

type Account struct {
	Provider     namespace.ProviderID
	ID           namespace.AccountID
	DisplayName  string
	DatabasePath string
}

// Adapter loads a complete, consistent metadata snapshot from one provider.
type Adapter interface {
	ID() namespace.ProviderID
	Capabilities() Capabilities
	DiscoverAccounts(context.Context) ([]Account, error)
	LoadSnapshot(context.Context, Account, uint64) (*namespace.Snapshot, error)
}
