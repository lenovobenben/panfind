package query

import (
	"fmt"
	"testing"
	"time"

	"github.com/lenovobenben/panfind/internal/namespace"
)

func TestParseAndExecuteTypeAndSize(t *testing.T) {
	root := namespace.NodeKey{Provider: "baidu-local", Account: "account-1", ID: 1}
	small := namespace.NodeKey{Provider: "baidu-local", Account: "account-1", ID: 2}
	large := namespace.NodeKey{Provider: "baidu-local", Account: "account-1", ID: 3}
	snapshot, err := namespace.NewSnapshot(1, root, []namespace.Node{
		{Key: root, Name: "/", Kind: namespace.NodeKindDirectory},
		{Key: small, Parent: root, Name: "small.bin", Kind: namespace.NodeKindFile, Size: 1024},
		{Key: large, Parent: root, Name: "large.bin", Kind: namespace.NodeKindFile, Size: 1024*1024*1024 + 1},
	})
	if err != nil {
		t.Fatal(err)
	}

	parsed, err := Parse("baidu:/", []string{"-type", "f", "-size", "+1G"})
	if err != nil {
		t.Fatal(err)
	}
	results, err := Execute(snapshot, parsed)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Path != "/large.bin" {
		t.Fatalf("Execute() = %+v", results)
	}
}

func TestParseRejectsUnsupportedToken(t *testing.T) {
	_, err := Parse("baidu:/", []string{"-newer", "reference.txt"})
	if err == nil {
		t.Fatal("Parse() accepted an unsupported predicate")
	}
}

func TestBooleanPrecedenceAndCaseInsensitiveName(t *testing.T) {
	root := namespace.NodeKey{Provider: "baidu-local", Account: "account-1", ID: 1}
	executable := namespace.NodeKey{Provider: "baidu-local", Account: "account-1", ID: 2}
	movie := namespace.NodeKey{Provider: "baidu-local", Account: "account-1", ID: 3}
	directory := namespace.NodeKey{Provider: "baidu-local", Account: "account-1", ID: 4}
	snapshot, err := namespace.NewSnapshot(1, root, []namespace.Node{
		{Key: root, Name: "/", Kind: namespace.NodeKindDirectory},
		{Key: executable, Parent: root, Name: "SETUP.EXE", Kind: namespace.NodeKindFile},
		{Key: movie, Parent: root, Name: "movie.mkv", Kind: namespace.NodeKindFile},
		{Key: directory, Parent: root, Name: "folder.exe", Kind: namespace.NodeKindDirectory},
	})
	if err != nil {
		t.Fatal(err)
	}

	// AND binds tighter than OR: (file AND *.exe) OR directory.
	parsed, err := Parse("baidu:/", []string{"-type", "f", "-iname", "*.exe", "-o", "-type", "d", "!", "-name", "/"})
	if err != nil {
		t.Fatal(err)
	}
	results, err := Execute(snapshot, parsed)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Path != "/SETUP.EXE" || results[1].Path != "/folder.exe" {
		t.Fatalf("Execute() = %+v", results)
	}
}

func TestParenthesesOverridePrecedence(t *testing.T) {
	parsed, err := Parse("baidu:/", []string{"-type", "f", "(", "-name", "*.mkv", "-o", "-name", "*.mp4", ")"})
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if _, ok := parsed.Expression.(And); !ok {
		t.Fatalf("Parse() expression = %T, want And", parsed.Expression)
	}
}

func TestParseErrors(t *testing.T) {
	tests := [][]string{
		{"("},
		{")"},
		{"(", ")"},
		{"-type"},
		{"-name", "["},
		{"-type", "f", "-o"},
		{"!"},
		{"-type", "f", "-maxdepth", "1"},
	}
	for _, tokens := range tests {
		if _, err := Parse("baidu:/", tokens); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", tokens)
		}
	}
}

func TestPathPredicateMatchesAcrossDirectories(t *testing.T) {
	root := namespace.NodeKey{Provider: "baidu-local", Account: "account-1", ID: 1}
	directory := namespace.NodeKey{Provider: "baidu-local", Account: "account-1", ID: 2}
	file := namespace.NodeKey{Provider: "baidu-local", Account: "account-1", ID: 3}
	snapshot, err := namespace.NewSnapshot(1, root, []namespace.Node{
		{Key: root, Name: "/", Kind: namespace.NodeKindDirectory},
		{Key: directory, Parent: root, Name: "software", Kind: namespace.NodeKindDirectory},
		{Key: file, Parent: directory, Name: "SETUP.EXE", Kind: namespace.NodeKindFile},
	})
	if err != nil {
		t.Fatal(err)
	}

	parsed, err := Parse("baidu:/", []string{"-type", "f", "-ipath", "baidu:/*.exe"})
	if err != nil {
		t.Fatal(err)
	}
	results, err := Execute(snapshot, parsed)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Path != "/software/SETUP.EXE" {
		t.Fatalf("Execute() = %+v", results)
	}
}

func TestDepthLimits(t *testing.T) {
	root := namespace.NodeKey{Provider: "baidu-local", Account: "account-1", ID: 1}
	directory := namespace.NodeKey{Provider: "baidu-local", Account: "account-1", ID: 2}
	file := namespace.NodeKey{Provider: "baidu-local", Account: "account-1", ID: 3}
	snapshot, err := namespace.NewSnapshot(1, root, []namespace.Node{
		{Key: root, Name: "/", Kind: namespace.NodeKindDirectory},
		{Key: directory, Parent: root, Name: "software", Kind: namespace.NodeKindDirectory},
		{Key: file, Parent: directory, Name: "setup.exe", Kind: namespace.NodeKindFile},
	})
	if err != nil {
		t.Fatal(err)
	}

	parsed, err := Parse("baidu:/", []string{"-mindepth", "1", "-maxdepth", "1", "-type", "d"})
	if err != nil {
		t.Fatal(err)
	}
	results, err := Execute(snapshot, parsed)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Path != "/software" {
		t.Fatalf("Execute() = %+v", results)
	}
}

func TestModificationTimePredicates(t *testing.T) {
	now := time.Date(2026, time.January, 10, 12, 0, 0, 0, time.UTC)
	root := namespace.NodeKey{Provider: "baidu-local", Account: "account-1", ID: 1}
	recentKey := namespace.NodeKey{Provider: "baidu-local", Account: "account-1", ID: 2}
	oneDayKey := namespace.NodeKey{Provider: "baidu-local", Account: "account-1", ID: 3}
	oldKey := namespace.NodeKey{Provider: "baidu-local", Account: "account-1", ID: 4}
	recent := now.Add(-12 * time.Hour)
	oneDay := now.Add(-36 * time.Hour)
	old := now.Add(-72 * time.Hour)
	snapshot, err := namespace.NewSnapshot(1, root, []namespace.Node{
		{Key: root, Name: "/", Kind: namespace.NodeKindDirectory},
		{Key: recentKey, Parent: root, Name: "recent", Kind: namespace.NodeKindFile, ModifiedAt: &recent},
		{Key: oneDayKey, Parent: root, Name: "one-day", Kind: namespace.NodeKindFile, ModifiedAt: &oneDay},
		{Key: oldKey, Parent: root, Name: "old", Kind: namespace.NodeKindFile, ModifiedAt: &old},
	})
	if err != nil {
		t.Fatal(err)
	}

	mtime, err := Parse("baidu:/", []string{"-mtime", "+1"})
	if err != nil {
		t.Fatal(err)
	}
	results, err := ExecuteAt(snapshot, mtime, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Path != "/old" {
		t.Fatalf("-mtime +1 results = %+v", results)
	}

	newer, err := Parse("baidu:/", []string{"-newermt", "2026-01-08T12:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	results, err = ExecuteAt(snapshot, newer, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Path != "/recent" || results[1].Path != "/one-day" {
		t.Fatalf("-newermt results = %+v", results)
	}
}

func TestExecuteFromSubdirectoryUsesRelativeDepth(t *testing.T) {
	root := namespace.NodeKey{Provider: "baidu-local", Account: "account-1", ID: 1}
	directory := namespace.NodeKey{Provider: "baidu-local", Account: "account-1", ID: 2}
	file := namespace.NodeKey{Provider: "baidu-local", Account: "account-1", ID: 3}
	sibling := namespace.NodeKey{Provider: "baidu-local", Account: "account-1", ID: 4}
	snapshot, err := namespace.NewSnapshot(1, root, []namespace.Node{
		{Key: root, Name: "/", Kind: namespace.NodeKindDirectory},
		{Key: directory, Parent: root, Name: "software", Kind: namespace.NodeKindDirectory},
		{Key: file, Parent: directory, Name: "setup.exe", Kind: namespace.NodeKindFile},
		{Key: sibling, Parent: root, Name: "outside.exe", Kind: namespace.NodeKindFile},
	})
	if err != nil {
		t.Fatal(err)
	}

	parsed, err := Parse("baidu:/software", []string{"-mindepth", "1", "-maxdepth", "1", "-type", "f"})
	if err != nil {
		t.Fatal(err)
	}
	results, err := Execute(snapshot, parsed)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Path != "/software/setup.exe" {
		t.Fatalf("Execute() = %+v", results)
	}
}

func TestExecuteEachStreamsAndPropagatesVisitorError(t *testing.T) {
	root := namespace.NodeKey{Provider: "baidu-local", Account: "account-1", ID: 1}
	file := namespace.NodeKey{Provider: "baidu-local", Account: "account-1", ID: 2}
	snapshot, err := namespace.NewSnapshot(1, root, []namespace.Node{
		{Key: root, Name: "/", Kind: namespace.NodeKindDirectory},
		{Key: file, Parent: root, Name: "file.bin", Kind: namespace.NodeKindFile},
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse("baidu:/", []string{"-type", "f"})
	if err != nil {
		t.Fatal(err)
	}

	want := fmt.Errorf("stop output")
	visits := 0
	err = ExecuteEach(snapshot, parsed, func(result Result) error {
		visits++
		if result.Path != "/file.bin" {
			t.Fatalf("result path = %q", result.Path)
		}
		return want
	})
	if err != want || visits != 1 {
		t.Fatalf("ExecuteEach() error = %v, visits = %d", err, visits)
	}
}
