package query

import (
	"strings"
	"testing"
)

func FuzzParse(f *testing.F) {
	for _, seed := range [][]string{
		nil,
		{"-type", "f", "-size", "+1G"},
		{"-type", "f", "(", "-iname", "*.mkv", "-o", "-iname", "*.mp4", ")"},
		{"-mindepth", "1", "-maxdepth", "3", "!", "-name", "*.tmp"},
		{"-newermt", "2025-01-01T00:00:00+08:00"},
	} {
		f.Add(strings.Join(seed, "\x00"))
	}

	f.Fuzz(func(t *testing.T, encoded string) {
		var tokens []string
		if encoded != "" {
			tokens = strings.Split(encoded, "\x00")
		}
		parsed, err := Parse("baidu:/", tokens)
		if err != nil {
			return
		}
		if _, err := Explain(parsed); err != nil {
			t.Fatalf("Explain(Parse(%q)) error: %v", tokens, err)
		}
	})
}
