package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/lenovobenben/panfind/internal/baidu"
	"github.com/lenovobenben/panfind/internal/output"
	"github.com/lenovobenben/panfind/internal/query"
	"github.com/lenovobenben/panfind/internal/version"
)

// Run executes the CLI and returns a process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		writeUsage(stderr)
		return 2
	}

	switch args[0] {
	case "help", "-h", "--help":
		writeUsage(stdout)
		return 0
	case "version", "-v", "--version":
		fmt.Fprintf(stdout, "panfind %s\n", version.Version)
		return 0
	case "capabilities":
		return runCapabilities(args[1:], stdout, stderr)
	case "status":
		return runStatus(args[1:], stdout, stderr)
	default:
		if strings.HasPrefix(args[0], "baidu:") {
			return runQuery(args, stdout, stderr)
		}
		fmt.Fprintf(stderr, "panfind: unsupported command or query %q\n", args[0])
		fmt.Fprintln(stderr, "Run 'panfind help' for usage.")
		return 2
	}
}

type queryResult struct {
	Path       string `json:"path"`
	Type       string `json:"type"`
	Size       uint64 `json:"size"`
	ModifiedAt any    `json:"modified_at,omitempty"`
}

func runQuery(args []string, stdout, stderr io.Writer) int {
	if args[0] != "baidu:/" {
		fmt.Fprintf(stderr, "panfind query: unsupported root %q; currently only baidu:/ is supported\n", args[0])
		return 2
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
	parsed, err := query.Parse(args[0], tokens)
	if err != nil {
		fmt.Fprintf(stderr, "panfind query: %v\n", err)
		return 2
	}

	adapter, err := baidu.New()
	if err != nil {
		fmt.Fprintf(stderr, "panfind query: %v\n", err)
		return 1
	}
	accounts, err := adapter.DiscoverAccounts(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "panfind query: %v\n", err)
		return 1
	}
	if len(accounts) == 0 {
		fmt.Fprintln(stderr, "panfind query: no Baidu Netdisk account database found")
		return 1
	}
	if len(accounts) > 1 {
		fmt.Fprintln(stderr, "panfind query: multiple accounts found; account selection is not implemented yet")
		return 1
	}

	snapshot, err := adapter.LoadSnapshot(context.Background(), accounts[0], 1)
	if err != nil {
		fmt.Fprintf(stderr, "panfind query: %v\n", err)
		return 1
	}
	results, err := query.Execute(snapshot, parsed)
	if err != nil {
		fmt.Fprintf(stderr, "panfind query: %v\n", err)
		return 1
	}

	for _, result := range results {
		cloudPath := "baidu:" + result.Path
		if !jsonOutput {
			if _, err := fmt.Fprintln(stdout, cloudPath); err != nil {
				if output.IsClosedPipe(err) {
					return 0
				}
				fmt.Fprintf(stderr, "panfind query: %v\n", err)
				return 1
			}
			continue
		}
		item := queryResult{
			Path: cloudPath,
			Type: result.Node.Kind.String(),
			Size: result.Node.Size,
		}
		if result.Node.ModifiedAt != nil {
			item.ModifiedAt = result.Node.ModifiedAt
		}
		if err := output.WriteJSONLine(stdout, item); err != nil {
			if output.IsClosedPipe(err) {
				return 0
			}
			fmt.Fprintf(stderr, "panfind query: %v\n", err)
			return 1
		}
	}
	return 0
}

func writeUsage(w io.Writer) {
	fmt.Fprintln(w, `PanFind (盘寻) — POSIX-style metadata search for cloud drives

Usage:
  panfind <root> [expression]   search cloud-drive metadata
  panfind status [--json]      discover accounts and load snapshot status
  panfind capabilities [--json] show provider capabilities
  panfind version              show version
  panfind help                 show this help

Supported expressions:
  -type f|d   -name PATTERN   -iname PATTERN   -size N[cwbkMG]
  -path PATTERN   -ipath PATTERN   -mtime N   -newermt DATE
  -mindepth N and -maxdepth N must appear before predicates
  ! EXPR      EXPR -a EXPR    EXPR -o EXPR     ( EXPR )`)
}

func runCapabilities(args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 || len(args) == 1 && args[0] != "--json" {
		fmt.Fprintln(stderr, "panfind capabilities: only --json is supported")
		return 2
	}

	adapter, err := baidu.New()
	if err != nil {
		fmt.Fprintf(stderr, "panfind capabilities: %v\n", err)
		return 1
	}
	capabilities := adapter.Capabilities()
	if len(args) == 1 {
		if err := json.NewEncoder(stdout).Encode(capabilities); err != nil {
			fmt.Fprintf(stderr, "panfind capabilities: %v\n", err)
			return 1
		}
		return 0
	}

	fmt.Fprintf(stdout, "provider=%s size=%t modified_at=%t created_at=%t added_at=%t hash=%t stable_id=%t incremental_hint=%t\n",
		adapter.ID(), capabilities.Size, capabilities.ModifiedAt, capabilities.CreatedAt,
		capabilities.AddedAt, capabilities.Hash, capabilities.StableID, capabilities.IncrementalHint)
	return 0
}

type accountStatus struct {
	Provider    string `json:"provider"`
	Account     string `json:"account"`
	Generation  uint64 `json:"generation"`
	Nodes       int    `json:"nodes"`
	Files       int    `json:"files"`
	Directories int    `json:"directories"`
}

func runStatus(args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 || len(args) == 1 && args[0] != "--json" {
		fmt.Fprintln(stderr, "panfind status: only --json is supported")
		return 2
	}

	adapter, err := baidu.New()
	if err != nil {
		fmt.Fprintf(stderr, "panfind status: %v\n", err)
		return 1
	}
	accounts, err := adapter.DiscoverAccounts(context.Background())
	if err != nil {
		fmt.Fprintf(stderr, "panfind status: %v\n", err)
		return 1
	}

	statuses := make([]accountStatus, 0, len(accounts))
	for _, account := range accounts {
		snapshot, err := adapter.LoadSnapshot(context.Background(), account, 1)
		if err != nil {
			fmt.Fprintf(stderr, "panfind status: load account %q: %v\n", account.ID, err)
			return 1
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
			return 1
		}
		return 0
	}
	if len(statuses) == 0 {
		fmt.Fprintln(stdout, "no Baidu Netdisk account database found")
		return 0
	}
	for _, status := range statuses {
		fmt.Fprintf(stdout, "provider=%s account=%s generation=%d nodes=%d files=%d directories=%d\n",
			status.Provider, status.Account, status.Generation, status.Nodes, status.Files, status.Directories)
	}
	return 0
}
