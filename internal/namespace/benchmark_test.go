package namespace_test

import (
	"runtime"
	"strconv"
	"testing"

	"github.com/lenovobenben/panfind/internal/namespace"
	"github.com/lenovobenben/panfind/internal/testkit"
)

func BenchmarkNewSnapshot(b *testing.B) {
	for _, fileCount := range []int{100_000, 1_000_000} {
		b.Run(strconv.Itoa(fileCount), func(b *testing.B) {
			root, nodes := testkit.FlatFiles(fileCount)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				snapshot, err := namespace.NewSnapshot(1, root, nodes)
				if err != nil {
					b.Fatal(err)
				}
				runtime.KeepAlive(snapshot)
			}
		})
	}
}

func BenchmarkNewDeepSnapshot(b *testing.B) {
	root, nodes := testkit.DeepDirectories(1_000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		snapshot, err := namespace.NewSnapshot(1, root, nodes)
		if err != nil {
			b.Fatal(err)
		}
		runtime.KeepAlive(snapshot)
	}
}
