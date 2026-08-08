package app

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunHelp(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Run([]string{"help"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("Run(help) returned %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "POSIX-style metadata search") {
		t.Fatalf("help output does not describe PanFind: %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("Run(help) wrote to stderr: %q", stderr.String())
	}
}

func TestRunWithoutArgumentsIsUsageError(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Run(nil, &stdout, &stderr)

	if code != 2 {
		t.Fatalf("Run(nil) returned %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("usage error does not contain usage: %q", stderr.String())
	}
}
