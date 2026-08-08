package output

import (
	"testing"
	"time"

	"github.com/lenovobenben/panfind/internal/namespace"
)

func TestPrintf(t *testing.T) {
	modified := time.Date(2026, time.January, 2, 3, 4, 5, 6, time.UTC)
	node := namespace.Node{
		Name:       "movie.mkv",
		Kind:       namespace.NodeKindFile,
		Size:       1024,
		ModifiedAt: &modified,
	}

	got, err := Printf(`%p\t%f\t%s\t%y\t%T+\n`, "baidu:/shows/movie.mkv", node)
	if err != nil {
		t.Fatal(err)
	}
	want := "baidu:/shows/movie.mkv\tmovie.mkv\t1024\tf\t2026-01-02T03:04:05.000000006Z\n"
	if got != want {
		t.Fatalf("Printf() = %q, want %q", got, want)
	}
}

func TestPrintfRejectsUnsupportedDirective(t *testing.T) {
	if _, err := Printf("%q", "baidu:/", namespace.Node{}); err == nil {
		t.Fatal("Printf() accepted unsupported directive")
	}
}
