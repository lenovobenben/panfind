package namespace

import (
	"fmt"
	"path"
)

// Snapshot is an immutable generation of a provider namespace.
// The constructor copies all caller-owned data before publishing it.
type Snapshot struct {
	generation uint64
	root       NodeKey
	nodes      map[NodeKey]Node
	children   map[NodeKey][]NodeKey
}

type Stats struct {
	Nodes       int
	Files       int
	Directories int
}

func NewSnapshot(generation uint64, root NodeKey, source []Node) (*Snapshot, error) {
	nodes := make(map[NodeKey]Node, len(source))
	children := make(map[NodeKey][]NodeKey)

	for _, node := range source {
		if _, exists := nodes[node.Key]; exists {
			return nil, fmt.Errorf("duplicate node key: %+v", node.Key)
		}
		nodes[node.Key] = cloneNode(node)
	}

	if _, exists := nodes[root]; !exists {
		return nil, fmt.Errorf("root node does not exist: %+v", root)
	}

	for _, sourceNode := range source {
		node := nodes[sourceNode.Key]
		if node.Key == root {
			continue
		}
		if _, exists := nodes[node.Parent]; !exists {
			return nil, fmt.Errorf("parent of node %+v does not exist: %+v", node.Key, node.Parent)
		}
		children[node.Parent] = append(children[node.Parent], node.Key)
	}

	return &Snapshot{
		generation: generation,
		root:       root,
		nodes:      nodes,
		children:   children,
	}, nil
}

func (s *Snapshot) Generation() uint64 {
	return s.generation
}

func (s *Snapshot) Root() NodeKey {
	return s.root
}

func (s *Snapshot) Len() int {
	return len(s.nodes)
}

func (s *Snapshot) Stats() Stats {
	stats := Stats{Nodes: len(s.nodes)}
	for _, node := range s.nodes {
		switch node.Kind {
		case NodeKindFile:
			stats.Files++
		case NodeKindDirectory:
			stats.Directories++
		}
	}
	return stats
}

// DescendantStats reports provider nodes without the namespace root.
func (s *Snapshot) DescendantStats() Stats {
	stats := s.Stats()
	root, exists := s.nodes[s.root]
	if !exists {
		return stats
	}
	stats.Nodes--
	switch root.Kind {
	case NodeKindFile:
		stats.Files--
	case NodeKindDirectory:
		stats.Directories--
	}
	return stats
}

func (s *Snapshot) Node(key NodeKey) (Node, bool) {
	node, ok := s.nodes[key]
	return cloneNode(node), ok
}

func (s *Snapshot) Children(key NodeKey) []NodeKey {
	return append([]NodeKey(nil), s.children[key]...)
}

// Walk returns start and all descendants in deterministic depth-first order.
func (s *Snapshot) Walk(start NodeKey) ([]NodeKey, error) {
	if _, exists := s.nodes[start]; !exists {
		return nil, fmt.Errorf("walk start node does not exist: %+v", start)
	}

	result := make([]NodeKey, 0, len(s.nodes))
	stack := []NodeKey{start}
	for len(stack) > 0 {
		last := len(stack) - 1
		key := stack[last]
		stack = stack[:last]
		result = append(result, key)

		children := s.children[key]
		for i := len(children) - 1; i >= 0; i-- {
			stack = append(stack, children[i])
		}
	}
	return result, nil
}

// Path returns a POSIX-style absolute path within the snapshot.
func (s *Snapshot) Path(key NodeKey) (string, error) {
	if key == s.root {
		return "/", nil
	}

	parts := make([]string, 0)
	current := key
	for steps := 0; steps < len(s.nodes); steps++ {
		node, exists := s.nodes[current]
		if !exists {
			return "", fmt.Errorf("path node does not exist: %+v", current)
		}
		parts = append(parts, node.Name)
		if node.Parent == s.root {
			result := "/"
			for i := len(parts) - 1; i >= 0; i-- {
				result = path.Join(result, parts[i])
			}
			return result, nil
		}
		current = node.Parent
	}
	return "", fmt.Errorf("parent cycle detected for node: %+v", key)
}

func cloneNode(node Node) Node {
	node.ModifiedAt = clonePointer(node.ModifiedAt)
	node.CreatedAt = clonePointer(node.CreatedAt)
	node.AddedAt = clonePointer(node.AddedAt)
	node.FirstSeenAt = clonePointer(node.FirstSeenAt)
	node.Hash = clonePointer(node.Hash)
	node.Category = clonePointer(node.Category)
	return node
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
