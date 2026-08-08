package app

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lenovobenben/panfind/internal/namespace"
	"github.com/lenovobenben/panfind/internal/provider"
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

func TestRunWatchRequiresRoot(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run([]string{"watch"}, &stdout, &stderr)
	if code != ExitUsage || !strings.Contains(stderr.String(), "panfind watch: missing query root") {
		t.Fatalf("Run(watch) = %d, stderr=%q", code, stderr.String())
	}
}

func TestWatchJSONResultsIncludeGeneration(t *testing.T) {
	root := namespace.NodeKey{Provider: "baidu-local", Account: "test", ID: 1}
	file := namespace.NodeKey{Provider: root.Provider, Account: root.Account, ID: 2}
	snapshot, err := namespace.NewSnapshot(7, root, []namespace.Node{
		{Key: root, Name: "/", Kind: namespace.NodeKindDirectory},
		{Key: file, Parent: root, Name: "movie.mkv", Kind: namespace.NodeKindFile, Size: 42},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := parseQueryRequest([]string{"baidu:/", "-type", "f", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	result := executeQuery(snapshot, request, snapshot.Generation(), &stdout)
	if result.err != nil || result.matched != 1 {
		t.Fatalf("executeQuery() = %+v", result)
	}
	var item struct {
		Generation uint64 `json:"generation"`
		Path       string `json:"path"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	if item.Generation != 7 || item.Path != "baidu:/movie.mkv" {
		t.Fatalf("watch result = %+v", item)
	}
}

func TestParseQueryAccount(t *testing.T) {
	request, err := parseQueryRequest([]string{"baidu:/", "--account", "second", "-type", "f", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	if request.accountID == nil || *request.accountID != "second" || !request.jsonOutput {
		t.Fatalf("parsed request = %+v", request)
	}
	if _, err := parseQueryRequest([]string{"baidu:/", "--account"}); err == nil {
		t.Fatal("missing account ID was accepted")
	}
}

type fakeAccountDiscoverer struct {
	accounts []provider.Account
	err      error
}

func (fake fakeAccountDiscoverer) DiscoverAccounts(context.Context) ([]provider.Account, error) {
	return fake.accounts, fake.err
}

func TestDiscoverQueryAccount(t *testing.T) {
	accounts := []provider.Account{
		{Provider: "baidu-local", ID: "first"},
		{Provider: "baidu-local", ID: "second"},
	}
	discoverer := fakeAccountDiscoverer{accounts: accounts}
	if _, err := discoverQueryAccount(context.Background(), discoverer, nil); err == nil || !strings.Contains(err.Error(), "use --account") {
		t.Fatalf("multiple account error = %v", err)
	}
	selected := namespace.AccountID("second")
	account, err := discoverQueryAccount(context.Background(), discoverer, &selected)
	if err != nil {
		t.Fatal(err)
	}
	if account.ID != selected {
		t.Fatalf("selected account = %q", account.ID)
	}
	missing := namespace.AccountID("missing")
	if _, err := discoverQueryAccount(context.Background(), discoverer, &missing); err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("missing account error = %v", err)
	}
}

func TestWriteAccountsJSON(t *testing.T) {
	accounts := []provider.Account{{Provider: "baidu-local", ID: "account-1", DisplayName: "Account One"}}
	var stdout bytes.Buffer
	if err := writeAccounts(&stdout, accounts, true); err != nil {
		t.Fatal(err)
	}
	var items []accountInfo
	if err := json.Unmarshal(stdout.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Account != "account-1" || items[0].DisplayName != "Account One" {
		t.Fatalf("account list = %+v", items)
	}
}

func TestExitCodeContract(t *testing.T) {
	if ExitSuccess != 0 || ExitNoMatches != 1 || ExitUsage != 2 || ExitDataSource != 3 || ExitOutput != 4 {
		t.Fatalf("exit code contract changed: %d %d %d %d %d", ExitSuccess, ExitNoMatches, ExitUsage, ExitDataSource, ExitOutput)
	}
}
