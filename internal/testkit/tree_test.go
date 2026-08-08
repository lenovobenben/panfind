package testkit

import "testing"

func TestSyntheticTreesBuild(t *testing.T) {
	root, flat := FlatFiles(10)
	if len(flat) != 11 || flat[0].Key != root {
		t.Fatalf("FlatFiles() returned %d nodes", len(flat))
	}
	root, deep := DeepDirectories(10)
	if len(deep) != 11 || deep[0].Key != root || deep[len(deep)-1].Parent != deep[len(deep)-2].Key {
		t.Fatalf("DeepDirectories() returned an invalid chain")
	}
}
