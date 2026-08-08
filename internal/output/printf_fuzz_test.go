package output

import (
	"testing"

	"github.com/lenovobenben/panfind/internal/namespace"
)

func FuzzPrintf(f *testing.F) {
	for _, seed := range []string{`%p\n`, `%f\t%s\t%y\n`, `%T+`, `%%`, `\0`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, format string) {
		_, _ = Printf(format, "baidu:/synthetic/file.bin", namespace.Node{
			Name: "file.bin",
			Kind: namespace.NodeKindFile,
			Size: 1024,
		})
	})
}
