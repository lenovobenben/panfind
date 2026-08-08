package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/lenovobenben/panfind/internal/baidu"
	"github.com/lenovobenben/panfind/internal/namespace"
	"github.com/lenovobenben/panfind/internal/output"
	"github.com/lenovobenben/panfind/internal/provider"
	"github.com/lenovobenben/panfind/internal/query"
	"github.com/lenovobenben/panfind/internal/syncer"
	"github.com/lenovobenben/panfind/internal/version"
)

// Run executes the CLI and returns a process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	return RunContext(context.Background(), args, stdout, stderr)
}

// RunContext executes the CLI with cancellation support for long-running
// commands such as watch.
func RunContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		writeUsage(stderr)
		return ExitUsage
	}

	switch args[0] {
	case "help", "-h", "--help":
		writeUsage(stdout)
		return ExitSuccess
	case "version", "-v", "--version":
		fmt.Fprintf(stdout, "panfind %s\n", version.Version)
		return ExitSuccess
	case "capabilities":
		return runCapabilities(args[1:], stdout, stderr)
	case "accounts":
		return runAccounts(ctx, args[1:], stdout, stderr)
	case "status":
		return runStatus(ctx, args[1:], stdout, stderr)
	case "schema":
		return runSchema(args[1:], stdout, stderr)
	case "explain":
		return runExplain(args[1:], stdout, stderr)
	case "query":
		return runQuery(ctx, args[1:], stdout, stderr)
	case "watch":
		return runWatch(ctx, args[1:], stdout, stderr)
	default:
		if strings.HasPrefix(args[0], "baidu:") {
			return runQuery(ctx, args, stdout, stderr)
		}
		fmt.Fprintf(stderr, "panfind: unsupported command or query %q\n", args[0])
		fmt.Fprintln(stderr, "Run 'panfind help' for usage.")
		return ExitUsage
	}
}

type queryResult struct {
	Generation uint64  `json:"generation,omitempty"`
	Path       string  `json:"path"`
	Type       string  `json:"type"`
	Size       uint64  `json:"size"`
	ModifiedAt any     `json:"modified_at,omitempty"`
	Hash       *string `json:"hash,omitempty"`
}

type queryRequest struct {
	query        query.Query
	jsonOutput   bool
	printfFormat *string
	accountID    *namespace.AccountID
}

func parseQueryRequest(args []string) (queryRequest, error) {
	if len(args) == 0 {
		return queryRequest{}, fmt.Errorf("missing query root")
	}
	if !strings.HasPrefix(args[0], "baidu:") {
		return queryRequest{}, fmt.Errorf("unsupported root %q; expected baidu:/ or baidu:/path", args[0])
	}

	jsonOutput := false
	tokens := make([]string, 0, len(args)-1)
	for _, token := range args[1:] {
		if token == "--json" {
			jsonOutput = true
			continue
		}
		tokens = append(tokens, token)
	}
	var accountID *namespace.AccountID
	if len(tokens) > 0 && tokens[0] == "--account" {
		if len(tokens) == 1 || tokens[1] == "" {
			return queryRequest{}, fmt.Errorf("--account requires an account ID")
		}
		selected := namespace.AccountID(tokens[1])
		accountID = &selected
		tokens = tokens[2:]
	}
	tokens, printfFormat, err := extractOutputAction(args[0], tokens)
	if err != nil {
		return queryRequest{}, err
	}
	if jsonOutput && printfFormat != nil {
		return queryRequest{}, fmt.Errorf("--json and -printf cannot be used together")
	}
	parsed, err := query.Parse(args[0], tokens)
	if err != nil {
		return queryRequest{}, err
	}
	return queryRequest{query: parsed, jsonOutput: jsonOutput, printfFormat: printfFormat, accountID: accountID}, nil
}

func runQuery(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	request, err := parseQueryRequest(args)
	if err != nil {
		fmt.Fprintf(stderr, "panfind query: %v\n", err)
		return ExitUsage
	}
	adapter, err := baidu.New()
	if err != nil {
		fmt.Fprintf(stderr, "panfind query: %v\n", err)
		return ExitDataSource
	}
	account, err := discoverQueryAccount(ctx, adapter, request.accountID)
	if err != nil {
		fmt.Fprintf(stderr, "panfind query: %v\n", err)
		return ExitDataSource
	}

	snapshot, err := adapter.LoadSnapshot(ctx, account, 1)
	if err != nil {
		fmt.Fprintf(stderr, "panfind query: %v\n", err)
		return ExitDataSource
	}
	result := executeQuery(snapshot, request, 0, stdout)
	if result.err != nil {
		if result.exitCode == ExitSuccess {
			return ExitSuccess
		}
		fmt.Fprintf(stderr, "panfind query: %v\n", result.err)
		return result.exitCode
	}
	if result.matched == 0 {
		return ExitNoMatches
	}
	return ExitSuccess
}

type queryExecution struct {
	matched  int
	err      error
	exitCode int
}

func executeQuery(snapshot *namespace.Snapshot, request queryRequest, generation uint64, stdout io.Writer) queryExecution {
	matched := 0
	var writeErr error
	var formatErr error
	err := query.ExecuteEach(snapshot, request.query, func(result query.Result) error {
		matched++
		cloudPath := "baidu:" + result.Path
		if request.printfFormat != nil {
			formatted, err := output.Printf(*request.printfFormat, cloudPath, result.Node)
			if err != nil {
				formatErr = err
				return err
			}
			if _, err := io.WriteString(stdout, formatted); err != nil {
				writeErr = err
				return err
			}
			return nil
		}
		if !request.jsonOutput {
			if _, err := fmt.Fprintln(stdout, cloudPath); err != nil {
				writeErr = err
				return err
			}
			return nil
		}
		item := queryResult{
			Generation: generation,
			Path:       cloudPath,
			Type:       result.Node.Kind.String(),
			Size:       result.Node.Size,
		}
		if result.Node.ModifiedAt != nil {
			item.ModifiedAt = result.Node.ModifiedAt
		}
		item.Hash = result.Node.Hash
		if err := output.WriteJSONLine(stdout, item); err != nil {
			writeErr = err
			return err
		}
		return nil
	})
	if err != nil {
		if formatErr != nil {
			return queryExecution{matched: matched, err: formatErr, exitCode: ExitUsage}
		}
		if writeErr != nil {
			if output.IsClosedPipe(writeErr) {
				return queryExecution{matched: matched, err: writeErr, exitCode: ExitSuccess}
			}
			return queryExecution{matched: matched, err: writeErr, exitCode: ExitOutput}
		}
		return queryExecution{matched: matched, err: err, exitCode: ExitDataSource}
	}
	return queryExecution{matched: matched, exitCode: ExitSuccess}
}

type accountDiscoverer interface {
	DiscoverAccounts(context.Context) ([]provider.Account, error)
}

func discoverQueryAccount(ctx context.Context, adapter accountDiscoverer, requested *namespace.AccountID) (provider.Account, error) {
	accounts, err := adapter.DiscoverAccounts(ctx)
	if err != nil {
		return provider.Account{}, err
	}
	if len(accounts) == 0 {
		return provider.Account{}, fmt.Errorf("no Baidu Netdisk account database found")
	}
	if requested != nil {
		for _, account := range accounts {
			if account.ID == *requested {
				return account, nil
			}
		}
		return provider.Account{}, fmt.Errorf("Baidu account %q was not found", *requested)
	}
	if len(accounts) > 1 {
		return provider.Account{}, fmt.Errorf("multiple accounts found; use --account <id> to select one")
	}
	return accounts[0], nil
}

func runWatch(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	request, err := parseQueryRequest(args)
	if err != nil {
		fmt.Fprintf(stderr, "panfind watch: %v\n", err)
		return ExitUsage
	}
	adapter, err := baidu.New()
	if err != nil {
		fmt.Fprintf(stderr, "panfind watch: %v\n", err)
		return ExitDataSource
	}
	account, err := discoverQueryAccount(ctx, adapter, request.accountID)
	if err != nil {
		fmt.Fprintf(stderr, "panfind watch: %v\n", err)
		return ExitDataSource
	}

	session := syncer.New(adapter, account)
	var observerResult queryExecution
	err = session.Run(ctx, syncer.WatchOptions{}, func(snapshot *namespace.Snapshot, status syncer.Status) error {
		if status.State == syncer.StateStale {
			fmt.Fprintf(stderr, "panfind watch: refresh failed; keeping generation=%d: %s\n", status.Generation, status.LastError)
			return nil
		}
		observerResult = executeQuery(snapshot, request, snapshot.Generation(), stdout)
		if observerResult.err != nil {
			return observerResult.err
		}
		fmt.Fprintf(stderr, "panfind watch: generation=%d matches=%d\n", snapshot.Generation(), observerResult.matched)
		return nil
	})
	if observerResult.err != nil {
		if observerResult.exitCode == ExitSuccess {
			return ExitSuccess
		}
		fmt.Fprintf(stderr, "panfind watch: %v\n", observerResult.err)
		return observerResult.exitCode
	}
	if errors.Is(err, context.Canceled) {
		return ExitSuccess
	}
	if err != nil {
		fmt.Fprintf(stderr, "panfind watch: %v\n", err)
		return ExitDataSource
	}
	return ExitSuccess
}

func extractOutputAction(root string, tokens []string) ([]string, *string, error) {
	if len(tokens) > 0 && tokens[len(tokens)-1] == "-print" {
		selection := tokens[:len(tokens)-1]
		if _, err := query.Parse(root, selection); err == nil {
			return selection, nil, nil
		}
	}
	if len(tokens) >= 2 && tokens[len(tokens)-2] == "-printf" {
		selection := tokens[:len(tokens)-2]
		if _, err := query.Parse(root, selection); err == nil {
			format := tokens[len(tokens)-1]
			if _, err := output.Printf(format, "baidu:/example", namespace.Node{}); err != nil {
				return nil, nil, err
			}
			return selection, &format, nil
		}
	}
	return tokens, nil, nil
}

func writeUsage(w io.Writer) {
	fmt.Fprintln(w, `PanFind (盘寻) — POSIX-style metadata search for cloud drives

Usage:
  panfind <root> [expression]   search cloud-drive metadata
  panfind query <root> [expression] explicit machine-friendly query form
  panfind watch <root> [expression] rerun a query after metadata changes
  panfind accounts [--json]    list discovered Baidu accounts
  panfind explain <root> [expression] [--json] show parsed query AST
  panfind schema [--json]      show the supported query language
  panfind status [--json]      discover accounts and load snapshot status
  panfind capabilities [--json] show provider capabilities
  panfind version              show version
  panfind help                 show this help

Account selection:
  Put --account ID immediately after <root>; a single discovered account is selected automatically.

Supported expressions:
  -type f|d   -name PATTERN   -iname PATTERN   -size N[cwbkMG]
  -path PATTERN   -ipath PATTERN   -mtime N   -newermt DATE
  -mindepth N and -maxdepth N must appear before predicates
  ! EXPR      EXPR -a EXPR    EXPR -o EXPR     ( EXPR )
  terminal actions: -print or -printf FORMAT`)
}

type accountInfo struct {
	Provider    string `json:"provider"`
	Account     string `json:"account"`
	DisplayName string `json:"display_name"`
}

func runAccounts(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	jsonOutput, err := optionalJSON(args)
	if err != nil {
		fmt.Fprintf(stderr, "panfind accounts: %v\n", err)
		return ExitUsage
	}
	adapter, err := baidu.New()
	if err != nil {
		fmt.Fprintf(stderr, "panfind accounts: %v\n", err)
		return ExitDataSource
	}
	accounts, err := adapter.DiscoverAccounts(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "panfind accounts: %v\n", err)
		return ExitDataSource
	}
	if err := writeAccounts(stdout, accounts, jsonOutput); err != nil {
		fmt.Fprintf(stderr, "panfind accounts: %v\n", err)
		return ExitOutput
	}
	return ExitSuccess
}

func writeAccounts(stdout io.Writer, accounts []provider.Account, jsonOutput bool) error {
	items := make([]accountInfo, 0, len(accounts))
	for _, account := range accounts {
		items = append(items, accountInfo{
			Provider:    string(account.Provider),
			Account:     string(account.ID),
			DisplayName: account.DisplayName,
		})
	}
	if jsonOutput {
		return json.NewEncoder(stdout).Encode(items)
	}
	if len(items) == 0 {
		_, err := fmt.Fprintln(stdout, "no Baidu Netdisk account database found")
		return err
	}
	for _, item := range items {
		if _, err := fmt.Fprintf(stdout, "provider=%s account=%s display_name=%q\n", item.Provider, item.Account, item.DisplayName); err != nil {
			return err
		}
	}
	return nil
}

func runSchema(args []string, stdout, stderr io.Writer) int {
	jsonOutput, err := optionalJSON(args)
	if err != nil {
		fmt.Fprintf(stderr, "panfind schema: %v\n", err)
		return ExitUsage
	}
	schema := schemaResponse{
		Schema: query.LanguageSchema(),
		ExitCodes: []exitCodeSpec{
			{Code: ExitSuccess, Name: "success", Semantics: "query produced at least one match or non-query command succeeded"},
			{Code: ExitNoMatches, Name: "no_matches", Semantics: "valid query produced no matches"},
			{Code: ExitUsage, Name: "usage_error", Semantics: "invalid command, query, option, or format"},
			{Code: ExitDataSource, Name: "data_source_error", Semantics: "account discovery, schema validation, database read, or namespace error"},
			{Code: ExitOutput, Name: "output_error", Semantics: "result output failed; a normal closed pipeline still exits successfully"},
		},
	}
	encoder := json.NewEncoder(stdout)
	if !jsonOutput {
		encoder.SetIndent("", "  ")
	}
	if err := encoder.Encode(schema); err != nil {
		fmt.Fprintf(stderr, "panfind schema: %v\n", err)
		return ExitOutput
	}
	return ExitSuccess
}

type schemaResponse struct {
	query.Schema
	ExitCodes []exitCodeSpec `json:"exit_codes"`
}

type exitCodeSpec struct {
	Code      int    `json:"code"`
	Name      string `json:"name"`
	Semantics string `json:"semantics"`
}

type explainedInvocation struct {
	query.Explanation
	Action string  `json:"action"`
	Format *string `json:"format,omitempty"`
}

func runExplain(args []string, stdout, stderr io.Writer) int {
	jsonOutput := false
	filtered := make([]string, 0, len(args))
	for _, argument := range args {
		if argument == "--json" {
			jsonOutput = true
			continue
		}
		filtered = append(filtered, argument)
	}
	if len(filtered) == 0 {
		fmt.Fprintln(stderr, "panfind explain: missing query root")
		return ExitUsage
	}
	if !strings.HasPrefix(filtered[0], "baidu:") {
		fmt.Fprintf(stderr, "panfind explain: unsupported root %q\n", filtered[0])
		return ExitUsage
	}

	tokens, printfFormat, err := extractOutputAction(filtered[0], filtered[1:])
	if err != nil {
		fmt.Fprintf(stderr, "panfind explain: %v\n", err)
		return ExitUsage
	}
	parsed, err := query.Parse(filtered[0], tokens)
	if err != nil {
		fmt.Fprintf(stderr, "panfind explain: %v\n", err)
		return ExitUsage
	}
	explanation, err := query.Explain(parsed)
	if err != nil {
		fmt.Fprintf(stderr, "panfind explain: %v\n", err)
		return ExitOutput
	}
	result := explainedInvocation{Explanation: explanation, Action: "print", Format: printfFormat}
	if printfFormat != nil {
		result.Action = "printf"
	}
	encoder := json.NewEncoder(stdout)
	if !jsonOutput {
		encoder.SetIndent("", "  ")
	}
	if err := encoder.Encode(result); err != nil {
		fmt.Fprintf(stderr, "panfind explain: %v\n", err)
		return ExitOutput
	}
	return ExitSuccess
}

func optionalJSON(args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	if len(args) == 1 && args[0] == "--json" {
		return true, nil
	}
	return false, fmt.Errorf("only --json is supported")
}

func runCapabilities(args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 || len(args) == 1 && args[0] != "--json" {
		fmt.Fprintln(stderr, "panfind capabilities: only --json is supported")
		return ExitUsage
	}

	adapter, err := baidu.New()
	if err != nil {
		fmt.Fprintf(stderr, "panfind capabilities: %v\n", err)
		return ExitDataSource
	}
	capabilities := adapter.Capabilities()
	if len(args) == 1 {
		if err := json.NewEncoder(stdout).Encode(capabilities); err != nil {
			fmt.Fprintf(stderr, "panfind capabilities: %v\n", err)
			return ExitOutput
		}
		return ExitSuccess
	}

	fmt.Fprintf(stdout, "provider=%s size=%t modified_at=%t created_at=%t added_at=%t hash=%t stable_id=%t incremental_hint=%t\n",
		adapter.ID(), capabilities.Size, capabilities.ModifiedAt, capabilities.CreatedAt,
		capabilities.AddedAt, capabilities.Hash, capabilities.StableID, capabilities.IncrementalHint)
	return ExitSuccess
}

type accountStatus struct {
	Provider    string `json:"provider"`
	Account     string `json:"account"`
	Generation  uint64 `json:"generation"`
	Nodes       int    `json:"nodes"`
	Files       int    `json:"files"`
	Directories int    `json:"directories"`
}

func runStatus(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 || len(args) == 1 && args[0] != "--json" {
		fmt.Fprintln(stderr, "panfind status: only --json is supported")
		return ExitUsage
	}

	adapter, err := baidu.New()
	if err != nil {
		fmt.Fprintf(stderr, "panfind status: %v\n", err)
		return ExitDataSource
	}
	accounts, err := adapter.DiscoverAccounts(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "panfind status: %v\n", err)
		return ExitDataSource
	}

	statuses := make([]accountStatus, 0, len(accounts))
	for _, account := range accounts {
		snapshot, err := adapter.LoadSnapshot(ctx, account, 1)
		if err != nil {
			fmt.Fprintf(stderr, "panfind status: load account %q: %v\n", account.ID, err)
			return ExitDataSource
		}
		stats := snapshot.DescendantStats()
		statuses = append(statuses, accountStatus{
			Provider:    string(account.Provider),
			Account:     string(account.ID),
			Generation:  snapshot.Generation(),
			Nodes:       stats.Nodes,
			Files:       stats.Files,
			Directories: stats.Directories,
		})
	}

	if len(args) == 1 {
		if err := json.NewEncoder(stdout).Encode(statuses); err != nil {
			fmt.Fprintf(stderr, "panfind status: %v\n", err)
			return ExitOutput
		}
		return ExitSuccess
	}
	if len(statuses) == 0 {
		fmt.Fprintln(stdout, "no Baidu Netdisk account database found")
		return ExitSuccess
	}
	for _, status := range statuses {
		fmt.Fprintf(stdout, "provider=%s account=%s generation=%d nodes=%d files=%d directories=%d\n",
			status.Provider, status.Account, status.Generation, status.Nodes, status.Files, status.Directories)
	}
	return ExitSuccess
}
