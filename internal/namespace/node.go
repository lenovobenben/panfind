// Package namespace defines PanFind's provider-neutral file metadata model.
package namespace

import "time"

type ProviderID string
type AccountID string

// NodeKey uniquely identifies a node across providers and accounts.
type NodeKey struct {
	Provider ProviderID
	Account  AccountID
	ID       int64
}

type NodeKind uint8

const (
	NodeKindUnknown NodeKind = iota
	NodeKindFile
	NodeKindDirectory
)

func (k NodeKind) String() string {
	switch k {
	case NodeKindFile:
		return "file"
	case NodeKindDirectory:
		return "directory"
	default:
		return "unknown"
	}
}

// Node is a provider-neutral view of one file or directory.
// Optional metadata is nil when the provider cannot supply it.
type Node struct {
	Key         NodeKey
	Parent      NodeKey
	Name        string
	Kind        NodeKind
	Size        uint64
	ModifiedAt  *time.Time
	CreatedAt   *time.Time
	AddedAt     *time.Time
	FirstSeenAt *time.Time
	Hash        *string
	Category    *int32
}
