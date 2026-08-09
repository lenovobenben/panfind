package quark

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lenovobenben/panfind/internal/namespace"
)

const defaultPageSize = 100

type listDirectoryRequest struct {
	DirectoryID string
	Page        int
	Size        int
}

type remoteNode struct {
	ID         string
	ParentID   string
	Name       string
	Kind       namespace.NodeKind
	Size       uint64
	ModifiedAt *time.Time
	CreatedAt  *time.Time
	Category   *int32
}

// directoryClient returns one already decoded page of direct children.
// Authentication and wire-protocol details remain private to its implementation.
type directoryClient interface {
	ListDirectory(context.Context, listDirectoryRequest) ([]remoteNode, error)
}

type scanner struct {
	store     *store
	client    directoryClient
	pageSize  int
	heartbeat func(context.Context) error
}

func newScanner(store *store, client directoryClient, pageSize int) (*scanner, error) {
	if store == nil {
		return nil, errors.New("Quark scanner store is nil")
	}
	if client == nil {
		return nil, errors.New("Quark directory client is nil")
	}
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	return &scanner{store: store, client: client, pageSize: pageSize}, nil
}

// run resumes a staging generation and publishes it only after every queued
// directory has been read to its terminal page.
func (s *scanner) run(ctx context.Context, runID int64) (*namespace.Snapshot, error) {
	for {
		if s.heartbeat != nil {
			if err := s.heartbeat(ctx); err != nil {
				return nil, err
			}
		}
		page, exists, err := s.store.nextCrawlPage(ctx, runID)
		if err != nil {
			return nil, err
		}
		if !exists {
			return s.store.publishGeneration(ctx, runID)
		}

		remoteNodes, err := s.client.ListDirectory(ctx, listDirectoryRequest{
			DirectoryID: page.RemoteID,
			Page:        page.Number,
			Size:        s.pageSize,
		})
		if err != nil {
			return nil, fmt.Errorf("list Quark directory %d page %d: %w", page.DirectoryID, page.Number, err)
		}
		if s.heartbeat != nil {
			if err := s.heartbeat(ctx); err != nil {
				return nil, err
			}
		}
		if len(remoteNodes) > s.pageSize {
			return nil, fmt.Errorf("Quark directory %d page %d returned %d nodes, limit %d", page.DirectoryID, page.Number, len(remoteNodes), s.pageSize)
		}

		crawled, err := s.convertPage(ctx, page, remoteNodes)
		if err != nil {
			return nil, err
		}
		complete := len(remoteNodes) < s.pageSize
		if err := s.store.commitCrawlPage(ctx, runID, page, crawled, complete); err != nil {
			return nil, err
		}
	}
}

func (s *scanner) convertPage(ctx context.Context, page crawlPage, remoteNodes []remoteNode) ([]crawledNode, error) {
	remoteIDs := make([]string, len(remoteNodes))
	seen := make(map[string]struct{}, len(remoteNodes))
	for index, remote := range remoteNodes {
		if remote.ID == "" {
			return nil, fmt.Errorf("Quark directory %d page %d node %d has an empty remote ID", page.DirectoryID, page.Number, index)
		}
		if remote.ParentID != page.RemoteID {
			return nil, fmt.Errorf("Quark directory %d page %d node %d has an unexpected parent", page.DirectoryID, page.Number, index)
		}
		if _, duplicate := seen[remote.ID]; duplicate {
			return nil, fmt.Errorf("Quark directory %d page %d contains duplicate node ID at index %d", page.DirectoryID, page.Number, index)
		}
		seen[remote.ID] = struct{}{}
		remoteIDs[index] = remote.ID
	}

	localIDs, err := s.store.resolveRemoteIDs(ctx, page.AccountID, remoteIDs)
	if err != nil {
		return nil, err
	}
	parent := namespace.NodeKey{Provider: ProviderID, Account: page.AccountID, ID: page.DirectoryID}
	result := make([]crawledNode, len(remoteNodes))
	for index, remote := range remoteNodes {
		result[index] = crawledNode{
			RemoteID: remote.ID,
			Node: namespace.Node{
				Key:        namespace.NodeKey{Provider: ProviderID, Account: page.AccountID, ID: localIDs[index]},
				Parent:     parent,
				Name:       remote.Name,
				Kind:       remote.Kind,
				Size:       remote.Size,
				ModifiedAt: remote.ModifiedAt,
				CreatedAt:  remote.CreatedAt,
				Category:   remote.Category,
			},
		}
	}
	return result, nil
}
