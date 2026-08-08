package namespace

import "testing"

func TestSnapshotCopiesInputAndResults(t *testing.T) {
	root := NodeKey{Provider: "baidu-local", Account: "account-1", ID: 1}
	file := NodeKey{Provider: "baidu-local", Account: "account-1", ID: 2}
	source := []Node{
		{Key: root, Name: "/", Kind: NodeKindDirectory},
		{Key: file, Parent: root, Name: "movie.mkv", Kind: NodeKindFile, Size: 1024},
	}

	snapshot, err := NewSnapshot(7, root, source)
	if err != nil {
		t.Fatalf("NewSnapshot() error: %v", err)
	}

	source[1].Name = "changed.mkv"
	node, ok := snapshot.Node(file)
	if !ok {
		t.Fatal("snapshot does not contain file")
	}
	if node.Name != "movie.mkv" {
		t.Fatalf("snapshot retained caller-owned data: got name %q", node.Name)
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
}
