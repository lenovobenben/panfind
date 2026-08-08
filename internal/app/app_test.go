package app

import (
	"bytes"
	"encoding/json"
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

func TestExtractPrintfAction(t *testing.T) {
	tokens, format, err := extractOutputAction("baidu:/", []string{"-type", "f", "-printf", `%p\n`})
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 || format == nil || *format != `%p\n` {
		t.Fatalf("extractOutputAction() = %q, %v", tokens, format)
	}
}

func TestExtractEmptyPrintfAction(t *testing.T) {
	_, format, err := extractOutputAction("baidu:/", []string{"-printf", ""})
	if err != nil {
		t.Fatal(err)
	}
	if format == nil || *format != "" {
		t.Fatalf("empty -printf format was not preserved: %v", format)
	}
}

func TestPrintCanBeNamePattern(t *testing.T) {
	tokens, format, err := extractOutputAction("baidu:/", []string{"-name", "-print"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 || format != nil {
		t.Fatalf("-print pattern was treated as an action: %q", tokens)
	}
}

func TestRunSchemaJSON(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run([]string{"schema", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run(schema) = %d, stderr=%q", code, stderr.String())
	}
	var schema struct {
		Version   int `json:"version"`
		ExitCodes []struct {
			Code int `json:"code"`
		} `json:"exit_codes"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &schema); err != nil {
		t.Fatalf("schema output is not JSON: %v", err)
	}
	if schema.Version != 1 {
		t.Fatalf("schema version = %d", schema.Version)
	}
	if len(schema.ExitCodes) != 5 || schema.ExitCodes[1].Code != ExitNoMatches {
		t.Fatalf("schema exit codes = %+v", schema.ExitCodes)
	}
}

func TestRunExplainJSON(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run([]string{"explain", "baidu:/shows", "-type", "f", "-size", "+1G", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run(explain) = %d, stderr=%q", code, stderr.String())
	}
	var explanation struct {
		Root       string `json:"root"`
		Expression struct {
			Operator string `json:"operator"`
		} `json:"expression"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &explanation); err != nil {
		t.Fatalf("explain output is not JSON: %v", err)
	}
	if explanation.Root != "baidu:/shows" || explanation.Expression.Operator != "and" {
		t.Fatalf("unexpected explanation: %+v", explanation)
	}
}

func TestRunQueryRequiresRoot(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run([]string{"query"}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "missing query root") {
		t.Fatalf("Run(query) = %d, stderr=%q", code, stderr.String())
	}
}

func TestExitCodeContract(t *testing.T) {
	if ExitSuccess != 0 || ExitNoMatches != 1 || ExitUsage != 2 || ExitDataSource != 3 || ExitOutput != 4 {
		t.Fatalf("exit code contract changed: %d %d %d %d %d", ExitSuccess, ExitNoMatches, ExitUsage, ExitDataSource, ExitOutput)
	}
}
