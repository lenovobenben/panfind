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
	"github.com/lenovobenben/panfind/internal/provider"
	"github.com/lenovobenben/panfind/internal/quark"
)

func queryProvider(root string) (string, string, error) {
	separator := strings.IndexByte(root, ':')
	if separator <= 0 {
		return "", "", fmt.Errorf("unsupported root %q; expected baidu:/ or quark:/", root)
	}
	name := root[:separator]
	switch name {
	case "baidu", "quark":
		return name, name + ":", nil
	default:
		return "", "", fmt.Errorf("unsupported root %q; expected baidu:/ or quark:/", root)
	}
}

func newProviderAdapter(name string) (provider.Adapter, error) {
	switch name {
	case "baidu":
		return baidu.New()
	case "quark":
		if !enableQuarkProvider {
			return nil, quarkProviderDisabledError()
		}
		return quark.New()
	default:
		return nil, fmt.Errorf("unsupported provider %q", name)
	}
}

func providerDisplayName(name string) string {
	switch name {
	case "baidu":
		return "Baidu Netdisk"
	case "quark":
		return "Quark Drive"
	default:
		return name
	}
}

func parseProviderCommandArgs(args []string) (string, bool, error) {
	name := "baidu"
	providerSet := false
	jsonOutput := false
	for _, argument := range args {
		switch argument {
		case "baidu", "quark":
			if providerSet {
				return "", false, errors.New("provider may only be specified once")
			}
			name = argument
			providerSet = true
		case "--json":
			if jsonOutput {
				return "", false, errors.New("--json may only be specified once")
			}
			jsonOutput = true
		default:
			return "", false, fmt.Errorf("unsupported argument %q", argument)
		}
	}
	return name, jsonOutput, nil
}

type quarkRefreshAdapter interface {
	ID() namespace.ProviderID
	Refresh(context.Context, func(quark.AuthorizationNotice)) (*namespace.Snapshot, error)
}

type quarkRefreshStatusAdapter interface {
	RefreshStatus(context.Context, provider.Account) (quark.RefreshStatus, error)
}

type refreshResult struct {
	Provider    string `json:"provider"`
	Account     string `json:"account"`
	Generation  uint64 `json:"generation"`
	Nodes       int    `json:"nodes"`
	Files       int    `json:"files"`
	Directories int    `json:"directories"`
}

func runRefresh(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "quark" {
		fmt.Fprintln(stderr, "panfind refresh: expected 'quark [--json]'")
		return ExitUsage
	}
	jsonOutput := false
	for _, argument := range args[1:] {
		if argument != "--json" || jsonOutput {
			fmt.Fprintln(stderr, "panfind refresh: only --json is supported after quark")
			return ExitUsage
		}
		jsonOutput = true
	}
	if !enableQuarkProvider {
		fmt.Fprintf(stderr, "panfind refresh: %v\n", quarkProviderDisabledError())
		return ExitDataSource
	}
	adapter, err := quark.New()
	if err != nil {
		fmt.Fprintf(stderr, "panfind refresh: %v\n", err)
		return ExitDataSource
	}
	return runQuarkRefresh(ctx, adapter, jsonOutput, stdout, stderr)
}

func runQuarkRefresh(ctx context.Context, adapter quarkRefreshAdapter, jsonOutput bool, stdout, stderr io.Writer) int {
	snapshot, err := adapter.Refresh(ctx, func(notice quark.AuthorizationNotice) {
		if notice.Reauthorization {
			if notice.PromptOpened {
				fmt.Fprintln(stderr, "panfind refresh: Quark session expired; confirm the new request in the desktop client")
				return
			}
			fmt.Fprintln(stderr, "panfind refresh: Quark session expired; waiting for a new desktop confirmation")
			return
		}
		if notice.PromptOpened {
			fmt.Fprintln(stderr, "panfind refresh: confirm the request in the Quark desktop client")
			return
		}
		fmt.Fprintln(stderr, "panfind refresh: waiting for confirmation in the Quark desktop client")
	})
	if err != nil {
		fmt.Fprintf(stderr, "panfind refresh: %v\n", err)
		return ExitDataSource
	}
	if snapshot == nil {
		fmt.Fprintln(stderr, "panfind refresh: Quark adapter returned no snapshot")
		return ExitDataSource
	}
	stats := snapshot.DescendantStats()
	result := refreshResult{
		Provider: string(adapter.ID()), Account: string(snapshot.Root().Account), Generation: snapshot.Generation(),
		Nodes: stats.Nodes, Files: stats.Files, Directories: stats.Directories,
	}
	if jsonOutput {
		if err := json.NewEncoder(stdout).Encode(result); err != nil {
			fmt.Fprintf(stderr, "panfind refresh: %v\n", err)
			return ExitOutput
		}
		return ExitSuccess
	}
	if _, err := fmt.Fprintf(stdout, "provider=%s account=%s generation=%d nodes=%d files=%d directories=%d\n",
		result.Provider, result.Account, result.Generation, result.Nodes, result.Files, result.Directories); err != nil {
		fmt.Fprintf(stderr, "panfind refresh: %v\n", err)
		return ExitOutput
	}
	return ExitSuccess
}
