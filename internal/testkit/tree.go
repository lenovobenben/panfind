// Package testkit creates anonymous, deterministic metadata trees for tests.
package testkit

import (
	"strconv"
	"time"

	"github.com/lenovobenben/panfind/internal/namespace"
)

const (
	Provider namespace.ProviderID = "synthetic"
	Account  namespace.AccountID  = "benchmark"
)

func RootKey() namespace.NodeKey {
	return namespace.NodeKey{Provider: Provider, Account: Account, ID: 0}
}

// FlatFiles returns a root followed by fileCount direct child files. Every
// second file is larger than 1 GiB to exercise selective size queries.
func FlatFiles(fileCount int) (namespace.NodeKey, []namespace.Node) {
	if fileCount < 0 {
		fileCount = 0
	}
	root := RootKey()
	nodes := make([]namespace.Node, 0, fileCount+1)
	nodes = append(nodes, namespace.Node{Key: root, Name: "/", Kind: namespace.NodeKindDirectory})
	modifiedAt := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	hash := "00000000000000000000000000000000"
	category := int32(1)
	for i := 0; i < fileCount; i++ {
		size := uint64(1024)
		if i%2 == 0 {
			size = 2 * 1024 * 1024 * 1024
		}
		nodes = append(nodes, namespace.Node{
			Key:        namespace.NodeKey{Provider: Provider, Account: Account, ID: int64(i + 1)},
			Parent:     root,
			Name:       "file-" + strconv.Itoa(i) + ".bin",
			Kind:       namespace.NodeKindFile,
			Size:       size,
			ModifiedAt: &modifiedAt,
			Hash:       &hash,
			Category:   &category,
		})
	}
	return root, nodes
}

// DeepDirectories returns a single directory chain below the root.
func DeepDirectories(depth int) (namespace.NodeKey, []namespace.Node) {
	if depth < 0 {
		depth = 0
	}
	root := RootKey()
	nodes := make([]namespace.Node, 0, depth+1)
	nodes = append(nodes, namespace.Node{Key: root, Name: "/", Kind: namespace.NodeKindDirectory})
	parent := root
	for i := 0; i < depth; i++ {
		key := namespace.NodeKey{Provider: Provider, Account: Account, ID: int64(i + 1)}
		nodes = append(nodes, namespace.Node{
			Key:    key,
			Parent: parent,
			Name:   "directory-" + strconv.Itoa(i),
			Kind:   namespace.NodeKindDirectory,
		})
		parent = key
	}
	return root, nodes
}
