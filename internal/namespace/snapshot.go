package namespace

import (
	"fmt"
	"path"
	"strings"
)

// Snapshot is an immutable generation of a provider namespace.
// The constructor copies all caller-owned data before publishing it.
type Snapshot struct {
	generation uint64
	root       NodeKey
	nodes      map[int64]snapshotNode
	children   map[int64][]int64
	paths      map[string]int64
}

type snapshotNode struct {
	node Node
	path string
}

type Stats struct {
	Nodes       int
	Files       int
	Directories int
}

func NewSnapshot(generation uint64, root NodeKey, source []Node) (*Snapshot, error) {
	nodes := make(map[int64]snapshotNode, len(source))
	children := make(map[int64][]int64)

	for _, node := range source {
		if node.Key.Provider != root.Provider || node.Key.Account != root.Account {
			return nil, fmt.Errorf("node key is outside snapshot scope: %+v", node.Key)
		}
		if _, exists := nodes[node.Key.ID]; exists {
			return nil, fmt.Errorf("duplicate node key: %+v", node.Key)
		}
		nodes[node.Key.ID] = snapshotNode{node: cloneNode(node)}
	}

	if _, exists := nodes[root.ID]; !exists {
		return nil, fmt.Errorf("root node does not exist: %+v", root)
	}

	for _, sourceNode := range source {
		node := nodes[sourceNode.Key.ID].node
		if node.Key == root {
			continue
		}
		if node.Parent.Provider != root.Provider || node.Parent.Account != root.Account {
			return nil, fmt.Errorf("parent of node %+v is outside snapshot scope: %+v", node.Key, node.Parent)
		}
		parent, exists := nodes[node.Parent.ID]
		if !exists {
			return nil, fmt.Errorf("parent of node %+v does not exist: %+v", node.Key, node.Parent)
		}
		if parent.node.Kind != NodeKindDirectory {
			return nil, fmt.Errorf("parent of node %+v is not a directory: %+v", node.Key, node.Parent)
		}
		children[node.Parent.ID] = append(children[node.Parent.ID], node.Key.ID)
	}

	snapshot := &Snapshot{
		generation: generation,
		root:       root,
		nodes:      nodes,
		children:   children,
		paths:      make(map[string]int64, len(nodes)),
	}
	snapshot.paths["/"] = root.ID
	rootNode := snapshot.nodes[root.ID]
	rootNode.path = "/"
	snapshot.nodes[root.ID] = rootNode
	visiting := make(map[int64]bool)
	for _, sourceNode := range source {
		if _, err := snapshot.indexPath(sourceNode.Key.ID, visiting); err != nil {
			return nil, err
		}
	}
	return snapshot, nil
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
	for _, record := range s.nodes {
		switch record.node.Kind {
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
	root, exists := s.nodes[s.root.ID]
	if !exists {
		return stats
	}
	stats.Nodes--
	switch root.node.Kind {
	case NodeKindFile:
		stats.Files--
	case NodeKindDirectory:
		stats.Directories--
	}
	return stats
}

func (s *Snapshot) Node(key NodeKey) (Node, bool) {
	if !s.containsKey(key) {
		return Node{}, false
	}
	record, ok := s.nodes[key.ID]
	return cloneNode(record.node), ok
}

func (s *Snapshot) Children(key NodeKey) []NodeKey {
	if !s.containsKey(key) {
		return nil
	}
	ids := s.children[key.ID]
	result := make([]NodeKey, len(ids))
	for index, id := range ids {
		result[index] = s.nodes[id].node.Key
	}
	return result
}

// Lookup resolves a POSIX-style absolute path.
func (s *Snapshot) Lookup(value string) (NodeKey, bool) {
	if value == "" {
		value = "/"
	}
	if !strings.HasPrefix(value, "/") || strings.ContainsRune(value, '\x00') {
		return NodeKey{}, false
	}
	id, exists := s.paths[path.Clean(value)]
	if !exists {
		return NodeKey{}, false
	}
	return s.nodes[id].node.Key, true
}

// Walk returns start and all descendants in deterministic depth-first order.
func (s *Snapshot) Walk(start NodeKey) ([]NodeKey, error) {
	result := make([]NodeKey, 0, len(s.nodes))
	err := s.WalkEach(start, func(key NodeKey) error {
		result = append(result, key)
		return nil
	})
	return result, err
}

// WalkEach visits start and all descendants in deterministic depth-first
// order. Its traversal stack grows with tree depth rather than node count.
func (s *Snapshot) WalkEach(start NodeKey, visit func(NodeKey) error) error {
	if visit == nil {
		return fmt.Errorf("walk visitor is nil")
	}
	if !s.containsKey(start) {
		return fmt.Errorf("walk start node does not exist: %+v", start)
	}
	if _, exists := s.nodes[start.ID]; !exists {
		return fmt.Errorf("walk start node does not exist: %+v", start)
	}
	if err := visit(s.nodes[start.ID].node.Key); err != nil {
		return err
	}

	type frame struct {
		id        int64
		nextChild int
	}
	stack := []frame{{id: start.ID}}
	for len(stack) > 0 {
		current := &stack[len(stack)-1]
		children := s.children[current.id]
		if current.nextChild >= len(children) {
			stack = stack[:len(stack)-1]
			continue
		}

		childID := children[current.nextChild]
		current.nextChild++
		if err := visit(s.nodes[childID].node.Key); err != nil {
			return err
		}
		stack = append(stack, frame{id: childID})
	}
	return nil
}

// Path returns a POSIX-style absolute path within the snapshot.
func (s *Snapshot) Path(key NodeKey) (string, error) {
	if !s.containsKey(key) {
		return "", fmt.Errorf("path node does not exist: %+v", key)
	}
	record, exists := s.nodes[key.ID]
	if !exists || record.path == "" {
		return "", fmt.Errorf("path node does not exist: %+v", key)
	}
	return record.path, nil
}

func (s *Snapshot) indexPath(id int64, visiting map[int64]bool) (string, error) {
	if record, exists := s.nodes[id]; exists && record.path != "" {
		return record.path, nil
	}
	if visiting[id] {
		return "", fmt.Errorf("parent cycle detected for node: %+v", s.nodes[id].node.Key)
	}
	record, exists := s.nodes[id]
	if !exists {
		return "", fmt.Errorf("path node ID does not exist: %d", id)
	}
	node := record.node
	if node.Name == "" || node.Name == "." || node.Name == ".." || strings.Contains(node.Name, "/") {
		return "", fmt.Errorf("invalid node name %q for node %+v", node.Name, node.Key)
	}

	parentPath := s.nodes[node.Parent.ID].path
	if parentPath == "" {
		visiting[id] = true
		var err error
		parentPath, err = s.indexPath(node.Parent.ID, visiting)
		if err != nil {
			return "", err
		}
		delete(visiting, id)
	}

	value := path.Join(parentPath, node.Name)
	if existingID, duplicate := s.paths[value]; duplicate && existingID != id {
		return "", fmt.Errorf("duplicate namespace path %q for nodes %+v and %+v", value, s.nodes[existingID].node.Key, node.Key)
	}
	s.paths[value] = id
	record.path = value
	s.nodes[id] = record
	return value, nil
}

func (s *Snapshot) containsKey(key NodeKey) bool {
	return key.Provider == s.root.Provider && key.Account == s.root.Account
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
