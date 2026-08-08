package query

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lenovobenben/panfind/internal/namespace"
)

func TestSupportedQueriesMatchGNUFind(t *testing.T) {
	gnuFind := locateGNUFind(t)
	now := time.Now().UTC().Truncate(time.Second)
	root := createDifferentialTree(t, now)
	snapshot := snapshotFromDirectory(t, root)
	newerReference := now.Add(-36 * time.Hour).Format(time.RFC3339)

	tests := []struct {
		name    string
		gnuArgs []string
		panArgs []string
	}{
		{name: "all nodes"},
		{name: "files", gnuArgs: []string{"-type", "f"}, panArgs: []string{"-type", "f"}},
		{name: "directories", gnuArgs: []string{"-type", "d"}, panArgs: []string{"-type", "d"}},
		{name: "case sensitive name", gnuArgs: []string{"-type", "f", "-name", "*.txt"}, panArgs: []string{"-type", "f", "-name", "*.txt"}},
		{name: "case insensitive name", gnuArgs: []string{"-type", "f", "-iname", "*.txt"}, panArgs: []string{"-type", "f", "-iname", "*.txt"}},
		{name: "name with spaces", gnuArgs: []string{"-type", "f", "-name", "*notes*"}, panArgs: []string{"-type", "f", "-name", "*notes*"}},
		{
			name:    "path",
			gnuArgs: []string{"-type", "f", "-path", "./media/movie.mkv"},
			panArgs: []string{"-type", "f", "-path", "baidu:/media/movie.mkv"},
		},
		{
			name:    "case insensitive path",
			gnuArgs: []string{"-type", "f", "-ipath", "./MEDIA/TRAILER.MP4"},
			panArgs: []string{"-type", "f", "-ipath", "baidu:/MEDIA/TRAILER.MP4"},
		},
		{name: "one kibibyte block", gnuArgs: []string{"-type", "f", "-size", "1k"}, panArgs: []string{"-type", "f", "-size", "1k"}},
		{name: "more than one kibibyte block", gnuArgs: []string{"-type", "f", "-size", "+1k"}, panArgs: []string{"-type", "f", "-size", "+1k"}},
		{name: "byte size", gnuArgs: []string{"-type", "f", "-size", "512c"}, panArgs: []string{"-type", "f", "-size", "512c"}},
		{name: "512 byte block", gnuArgs: []string{"-type", "f", "-size", "1b"}, panArgs: []string{"-type", "f", "-size", "1b"}},
		{name: "more than one mebibyte block", gnuArgs: []string{"-type", "f", "-size", "+1M"}, panArgs: []string{"-type", "f", "-size", "+1M"}},
		{name: "modified within one day", gnuArgs: []string{"-type", "f", "-mtime", "-1"}, panArgs: []string{"-type", "f", "-mtime", "-1"}},
		{name: "modified one day ago", gnuArgs: []string{"-type", "f", "-mtime", "1"}, panArgs: []string{"-type", "f", "-mtime", "1"}},
		{name: "modified more than one day ago", gnuArgs: []string{"-type", "f", "-mtime", "+1"}, panArgs: []string{"-type", "f", "-mtime", "+1"}},
		{
			name:    "newer than reference",
			gnuArgs: []string{"-type", "f", "-newermt", newerReference},
			panArgs: []string{"-type", "f", "-newermt", newerReference},
		},
		{
			name:    "depth range",
			gnuArgs: []string{"-mindepth", "1", "-maxdepth", "1", "-type", "f"},
			panArgs: []string{"-mindepth", "1", "-maxdepth", "1", "-type", "f"},
		},
		{
			name:    "boolean expression",
			gnuArgs: []string{"-type", "f", "(", "-iname", "*.mkv", "-o", "-iname", "*.mp4", ")", "-size", "+1k"},
			panArgs: []string{"-type", "f", "(", "-iname", "*.mkv", "-o", "-iname", "*.mp4", ")", "-size", "+1k"},
		},
		{
			name:    "explicit and",
			gnuArgs: []string{"-type", "f", "-a", "!", "-name", "*.zip"},
			panArgs: []string{"-type", "f", "-a", "!", "-name", "*.zip"},
		},
		{
			name:    "and precedence over or",
			gnuArgs: []string{"-name", "*.zip", "-o", "-name", "*.mkv", "-a", "-size", "+1k"},
			panArgs: []string{"-name", "*.zip", "-o", "-name", "*.mkv", "-a", "-size", "+1k"},
		},
		{name: "not directories", gnuArgs: []string{"!", "-type", "d"}, panArgs: []string{"!", "-type", "d"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			want := runGNUFind(t, gnuFind, root, test.gnuArgs)
			got := runPanFind(t, snapshot, now, test.panArgs)
			if !slices.Equal(got, want) {
				t.Fatalf("result mismatch\nPanFind: %q\nGNU find: %q", got, want)
			}
		})
	}
}

func locateGNUFind(t *testing.T) string {
	t.Helper()
	if configured := os.Getenv("PANFIND_GNU_FIND"); configured != "" {
		output, err := exec.Command(configured, "--version").CombinedOutput()
		if err != nil || !bytes.Contains(output, []byte("GNU findutils")) {
			t.Fatalf("PANFIND_GNU_FIND=%q is not GNU findutils: %v: %s", configured, err, strings.TrimSpace(string(output)))
		}
		return configured
	}

	candidates := make([]string, 0, 3)
	if runtime.GOOS == "windows" {
		if programFiles := os.Getenv("ProgramFiles"); programFiles != "" {
			candidates = append(candidates, filepath.Join(programFiles, "Git", "usr", "bin", "find.exe"))
		}
	}
	for _, name := range []string{"gfind", "find"} {
		if path, err := exec.LookPath(name); err == nil {
			candidates = append(candidates, path)
		}
	}

	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		output, err := exec.Command(candidate, "--version").CombinedOutput()
		if err == nil && bytes.Contains(output, []byte("GNU findutils")) {
			return candidate
		}
	}
	t.Skip("GNU findutils is not installed; set PANFIND_GNU_FIND to its executable")
	return ""
}

func createDifferentialTree(t *testing.T, now time.Time) string {
	t.Helper()
	root := t.TempDir()
	for _, directory := range []string{"docs", "empty", "media", "media/subtitles"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(directory)), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	files := []struct {
		path string
		size int64
		age  time.Duration
	}{
		{path: "root.bin", size: 512, age: 26 * time.Hour},
		{path: "docs/Guide.TXT", size: 100, age: 2 * time.Hour},
		{path: "docs/meeting notes.txt", size: 1025, age: 50 * time.Hour},
		{path: "media/movie.mkv", size: 1024*1024 + 1, age: 3 * time.Hour},
		{path: "media/Trailer.MP4", size: 2048, age: 74 * time.Hour},
		{path: "media/archive.zip", size: 1024, age: 10 * 24 * time.Hour},
		{path: "media/subtitles/movie.srt", size: 200, age: 4 * time.Hour},
	}
	for _, file := range files {
		path := filepath.Join(root, filepath.FromSlash(file.path))
		handle, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := handle.Truncate(file.size); err != nil {
			handle.Close()
			t.Fatal(err)
		}
		if err := handle.Close(); err != nil {
			t.Fatal(err)
		}
		modifiedAt := now.Add(-file.age)
		if err := os.Chtimes(path, modifiedAt, modifiedAt); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func snapshotFromDirectory(t *testing.T, root string) *namespace.Snapshot {
	t.Helper()
	providerID := namespace.ProviderID("differential")
	accountID := namespace.AccountID("test")
	rootKey := namespace.NodeKey{Provider: providerID, Account: accountID, ID: 1}
	nodes := []namespace.Node{{Key: rootKey, Name: "/", Kind: namespace.NodeKindDirectory}}
	keys := map[string]namespace.NodeKey{".": rootKey}
	nextID := int64(2)

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		parent := filepath.Dir(relative)
		parentKey, exists := keys[parent]
		if !exists {
			return fmt.Errorf("parent %q was not indexed", parent)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		key := namespace.NodeKey{Provider: providerID, Account: accountID, ID: nextID}
		nextID++
		kind := namespace.NodeKindFile
		if entry.IsDir() {
			kind = namespace.NodeKindDirectory
		}
		modifiedAt := info.ModTime()
		nodes = append(nodes, namespace.Node{
			Key: key, Parent: parentKey, Name: entry.Name(), Kind: kind,
			Size: uint64(info.Size()), ModifiedAt: &modifiedAt,
		})
		keys[relative] = key
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := namespace.NewSnapshot(1, rootKey, nodes)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func runGNUFind(t *testing.T, executable, root string, arguments []string) []string {
	t.Helper()
	command := exec.Command(executable, append([]string{"."}, arguments...)...)
	command.Dir = root
	command.Env = append(os.Environ(), "LC_ALL=C")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("GNU find %q failed: %v: %s", arguments, err, strings.TrimSpace(string(output)))
	}
	result := make([]string, 0)
	for _, line := range strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n") {
		if line == "" {
			continue
		}
		line = filepath.ToSlash(line)
		if line == "." {
			line = "/"
		} else {
			line = strings.TrimPrefix(line, ".")
		}
		result = append(result, line)
	}
	slices.Sort(result)
	return result
}

func runPanFind(t *testing.T, snapshot *namespace.Snapshot, now time.Time, arguments []string) []string {
	t.Helper()
	parsed, err := Parse("baidu:/", arguments)
	if err != nil {
		t.Fatal(err)
	}
	result := make([]string, 0)
	if err := ExecuteEachAt(snapshot, parsed, now, func(match Result) error {
		result = append(result, match.Path)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	slices.Sort(result)
	return result
}
