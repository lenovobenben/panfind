package namespace

import (
	"fmt"
	"testing"
	"time"
)

func TestSnapshotCopiesInputAndResults(t *testing.T) {
	root := NodeKey{Provider: "baidu-local", Account: "account-1", ID: 1}
	file := NodeKey{Provider: "baidu-local", Account: "account-1", ID: 2}
	modifiedAt := time.Date(2026, time.August, 8, 0, 0, 0, 0, time.UTC)
	hash := "original"
	category := int32(1)
	source := []Node{
		{Key: root, Name: "/", Kind: NodeKindDirectory},
		{
			Key: file, Parent: root, Name: "movie.mkv", Kind: NodeKindFile, Size: 1024,
			ModifiedAt: &modifiedAt, Hash: &hash, Category: &category,
		},
	}

	snapshot, err := NewSnapshot(7, root, source)
	if err != nil {
		t.Fatalf("NewSnapshot() error: %v", err)
	}

	source[1].Name = "changed.mkv"
	modifiedAt = modifiedAt.Add(time.Hour)
	hash = "changed"
	category = 2
	node, ok := snapshot.Node(file)
	if !ok {
		t.Fatal("snapshot does not contain file")
	}
	if node.Name != "movie.mkv" || node.ModifiedAt.Hour() != 0 || *node.Hash != "original" || *node.Category != 1 {
		t.Fatalf("snapshot retained caller-owned data: %+v", node)
	}
	*node.Hash = "result changed"
	*node.Category = 3
	if second, _ := snapshot.Node(file); *second.Hash != "original" || *second.Category != 1 {
		t.Fatalf("Node returned mutable snapshot storage: %+v", second)
	}

	children := snapshot.Children(root)
	children[0] = root
	if got := snapshot.Children(root)[0]; got != file {
		t.Fatalf("Children returned mutable snapshot storage: got %+v", got)
	}

	stats := snapshot.DescendantStats()
	if stats.Nodes != 1 || stats.Files != 1 || stats.Directories != 0 {
		t.Fatalf("DescendantStats() = %+v, want one file", stats)
	}
}

func TestSnapshotRejectsKeysOutsideRootScope(t *testing.T) {
	root := NodeKey{Provider: "baidu-local", Account: "account-1", ID: 1}
	foreign := NodeKey{Provider: "baidu-local", Account: "account-2", ID: 2}
	_, err := NewSnapshot(1, root, []Node{
		{Key: root, Name: "/", Kind: NodeKindDirectory},
		{Key: foreign, Parent: root, Name: "foreign", Kind: NodeKindFile},
	})
	if err == nil {
		t.Fatal("NewSnapshot() accepted a node outside the root provider/account scope")
	}

	file := NodeKey{Provider: "baidu-local", Account: "account-1", ID: 2}
	foreignParent := NodeKey{Provider: "baidu-local", Account: "account-2", ID: root.ID}
	_, err = NewSnapshot(1, root, []Node{
		{Key: root, Name: "/", Kind: NodeKindDirectory},
		{Key: file, Parent: foreignParent, Name: "foreign-parent", Kind: NodeKindFile},
	})
	if err == nil {
		t.Fatal("NewSnapshot() accepted a parent outside the root provider/account scope")
	}
}

func TestSnapshotDoesNotResolveForeignKeyWithSameID(t *testing.T) {
	root := NodeKey{Provider: "baidu-local", Account: "account-1", ID: 1}
	file := NodeKey{Provider: "baidu-local", Account: "account-1", ID: 2}
	foreign := NodeKey{Provider: "baidu-local", Account: "account-2", ID: file.ID}
	snapshot, err := NewSnapshot(1, root, []Node{
		{Key: root, Name: "/", Kind: NodeKindDirectory},
		{Key: file, Parent: root, Name: "file", Kind: NodeKindFile},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, exists := snapshot.Node(foreign); exists {
		t.Fatal("Node() resolved a key from another account")
	}
	if children := snapshot.Children(foreign); children != nil {
		t.Fatalf("Children() resolved a key from another account: %+v", children)
	}
	if _, err := snapshot.Path(foreign); err == nil {
		t.Fatal("Path() resolved a key from another account")
	}
	if err := snapshot.WalkEach(foreign, func(NodeKey) error { return nil }); err == nil {
		t.Fatal("WalkEach() resolved a key from another account")
	}
}

func TestSnapshotRejectsMissingParent(t *testing.T) {
	root := NodeKey{Provider: "baidu-local", Account: "account-1", ID: 1}
	missing := NodeKey{Provider: "baidu-local", Account: "account-1", ID: 99}
	_, err := NewSnapshot(1, root, []Node{
		{Key: root, Name: "/", Kind: NodeKindDirectory},
		{Key: NodeKey{Provider: "baidu-local", Account: "account-1", ID: 2}, Parent: missing, Name: "orphan"},
	})
	if err == nil {
		t.Fatal("NewSnapshot() accepted a missing parent")
	}
}

func TestSnapshotWalkAndPath(t *testing.T) {
	root := NodeKey{Provider: "baidu-local", Account: "account-1", ID: 1}
	directory := NodeKey{Provider: "baidu-local", Account: "account-1", ID: 2}
	file := NodeKey{Provider: "baidu-local", Account: "account-1", ID: 3}
	snapshot, err := NewSnapshot(1, root, []Node{
		{Key: root, Name: "/", Kind: NodeKindDirectory},
		{Key: directory, Parent: root, Name: "shows", Kind: NodeKindDirectory},
		{Key: file, Parent: directory, Name: "episode.mkv", Kind: NodeKindFile},
	})
	if err != nil {
		t.Fatal(err)
	}

	keys, err := snapshot.Walk(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 || keys[0] != root || keys[1] != directory || keys[2] != file {
		t.Fatalf("Walk() = %+v", keys)
	}
	filePath, err := snapshot.Path(file)
	if err != nil {
		t.Fatal(err)
	}
	if filePath != "/shows/episode.mkv" {
		t.Fatalf("Path(file) = %q", filePath)
	}
	lookedUp, exists := snapshot.Lookup("/shows/episode.mkv")
	if !exists || lookedUp != file {
		t.Fatalf("Lookup() = %+v, %t", lookedUp, exists)
	}
}

func TestSnapshotSupportsRootAfterItsChildrenInSource(t *testing.T) {
	root := NodeKey{Provider: "baidu-local", Account: "account-1", ID: 1}
	file := NodeKey{Provider: "baidu-local", Account: "account-1", ID: 2}
	snapshot, err := NewSnapshot(1, root, []Node{
		{Key: file, Parent: root, Name: "first-in-source", Kind: NodeKindFile},
		{Key: root, Name: "/", Kind: NodeKindDirectory},
	})
	if err != nil {
		t.Fatal(err)
	}

	keys, err := snapshot.Walk(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] != root || keys[1] != file {
		t.Fatalf("Walk() = %+v", keys)
	}
}

func TestSnapshotPreservesUnicodeNames(t *testing.T) {
	root := NodeKey{Provider: "baidu-local", Account: "account-1", ID: 1}
	directory := NodeKey{Provider: "baidu-local", Account: "account-1", ID: 2}
	file := NodeKey{Provider: "baidu-local", Account: "account-1", ID: 3}
	snapshot, err := NewSnapshot(1, root, []Node{
		{Key: root, Name: "/", Kind: NodeKindDirectory},
		{Key: directory, Parent: root, Name: "资料", Kind: NodeKindDirectory},
		{Key: file, Parent: directory, Name: "文件.txt", Kind: NodeKindFile},
	})
	if err != nil {
		t.Fatal(err)
	}

	node, exists := snapshot.Node(file)
	filePath, pathErr := snapshot.Path(file)
	lookedUp, found := snapshot.Lookup("/资料/文件.txt")
	if !exists || pathErr != nil || node.Name != "文件.txt" || filePath != "/资料/文件.txt" || !found || lookedUp != file {
		t.Fatalf("node = %+v, path = %q, pathErr = %v, lookup = %+v, found = %t", node, filePath, pathErr, lookedUp, found)
	}
}

func TestSnapshotRejectsInvalidNonRootNames(t *testing.T) {
	root := NodeKey{Provider: "baidu-local", Account: "account-1", ID: 1}
	for _, name := range []string{"", ".", "..", "a/b"} {
		t.Run(name, func(t *testing.T) {
			_, err := NewSnapshot(1, root, []Node{
				{Key: root, Name: "/", Kind: NodeKindDirectory},
				{Key: NodeKey{Provider: root.Provider, Account: root.Account, ID: 2}, Parent: root, Name: name, Kind: NodeKindFile},
			})
			if err == nil {
				t.Fatalf("NewSnapshot() accepted invalid name %q", name)
			}
		})
	}
}

func TestSnapshotRejectsDuplicatePath(t *testing.T) {
	root := NodeKey{Provider: "baidu-local", Account: "account-1", ID: 1}
	_, err := NewSnapshot(1, root, []Node{
		{Key: root, Name: "/", Kind: NodeKindDirectory},
		{Key: NodeKey{Provider: "baidu-local", Account: "account-1", ID: 2}, Parent: root, Name: "duplicate", Kind: NodeKindFile},
		{Key: NodeKey{Provider: "baidu-local", Account: "account-1", ID: 3}, Parent: root, Name: "duplicate", Kind: NodeKindFile},
	})
	if err == nil {
		t.Fatal("NewSnapshot() accepted duplicate paths")
	}
}

func TestSnapshotRejectsParentCycle(t *testing.T) {
	root := NodeKey{Provider: "baidu-local", Account: "account-1", ID: 1}
	first := NodeKey{Provider: "baidu-local", Account: "account-1", ID: 2}
	second := NodeKey{Provider: "baidu-local", Account: "account-1", ID: 3}
	_, err := NewSnapshot(1, root, []Node{
		{Key: root, Name: "/", Kind: NodeKindDirectory},
		{Key: first, Parent: second, Name: "first", Kind: NodeKindDirectory},
		{Key: second, Parent: first, Name: "second", Kind: NodeKindDirectory},
	})
	if err == nil {
		t.Fatal("NewSnapshot() accepted a parent cycle")
	}
}

func TestWalkEachStopsOnVisitorError(t *testing.T) {
	root := NodeKey{Provider: "baidu-local", Account: "account-1", ID: 1}
	child := NodeKey{Provider: "baidu-local", Account: "account-1", ID: 2}
	snapshot, err := NewSnapshot(1, root, []Node{
		{Key: root, Name: "/", Kind: NodeKindDirectory},
		{Key: child, Parent: root, Name: "child", Kind: NodeKindFile},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := fmt.Errorf("stop")
	visits := 0
	err = snapshot.WalkEach(root, func(NodeKey) error {
		visits++
		return want
	})
	if err != want || visits != 1 {
		t.Fatalf("WalkEach() error = %v, visits = %d", err, visits)
	}
}
