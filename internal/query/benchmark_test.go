package query_test

import (
	"runtime"
	"testing"

	"github.com/lenovobenben/panfind/internal/namespace"
	"github.com/lenovobenben/panfind/internal/query"
	"github.com/lenovobenben/panfind/internal/testkit"
)

func BenchmarkExecuteCollect100K(b *testing.B) {
	root, nodes := testkit.FlatFiles(100_000)
	snapshot, err := namespace.NewSnapshot(1, root, nodes)
	if err != nil {
		b.Fatal(err)
	}
	parsed, err := query.Parse("synthetic:/", []string{"-type", "f", "-size", "+1G"})
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		results, err := query.Execute(snapshot, parsed)
		if err != nil {
			b.Fatal(err)
		}
		runtime.KeepAlive(results)
	}
}

func BenchmarkExecuteStream100K(b *testing.B) {
	root, nodes := testkit.FlatFiles(100_000)
	snapshot, err := namespace.NewSnapshot(1, root, nodes)
	if err != nil {
		b.Fatal(err)
	}
	parsed, err := query.Parse("synthetic:/", []string{"-type", "f", "-size", "+1G"})
	if err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		matched := 0
		err := query.ExecuteEach(snapshot, parsed, func(query.Result) error {
			matched++
			return nil
		})
		if err != nil {
			b.Fatal(err)
		}
		runtime.KeepAlive(matched)
	}
}
