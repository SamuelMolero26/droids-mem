package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/samuelmolero26/droids-mem/internal/graph"
	"github.com/samuelmolero26/droids-mem/internal/state"
)

// graphTarget resolves a graph subcommand's target from either the positional
// arg or its name-matched flag (--symbol/--package), enforcing exactly one so
// the MCP arg names work on the CLI without a second, conflicting source.
func graphTarget(args []string, flagVal, kind string) (string, error) {
	switch {
	case len(args) == 1 && flagVal != "":
		return "", fmt.Errorf("pass the %s once: as an argument or via --%s, not both", kind, kind)
	case len(args) == 1:
		return args[0], nil
	case flagVal != "":
		return flagVal, nil
	default:
		return "", fmt.Errorf("provide a %s: as an argument (`graph %s <%s>`) or via --%s", kind, kind, kind, kind)
	}
}

// graphManager builds the code-graph manager rooted at <state dir>/graphs.
// Shared by `graph` subcommands and `serve` (ADR-0020).
func graphManager() (*graph.Manager, error) {
	dir, err := state.Dir()
	if err != nil {
		return nil, err
	}
	return graph.NewManager(filepath.Join(dir, "graphs")), nil
}

func newGraphCmd() *cobra.Command {
	var repo string
	cmd := &cobra.Command{
		Use:   "graph",
		Short: "Query the code graph of a Go repo (symbols, call edges)",
		Long: `graph indexes a Go repo's symbols and call edges (interface dispatch
resolved) and answers surgical code questions without file crawling.

The graph is stored per repo under ~/.droids-mem/graphs/ and rebuilt
automatically when the repo changes. See docs/adr/0020-native-code-graph.md.`,
	}
	// The code graph never touches mem.db — no boot gate, no store. Cobra
	// annotations don't inherit, so each leaf carries the bypass itself.
	bypass := map[string]string{bootGateBypass: "true"}
	cmd.PersistentFlags().StringVar(&repo, "repo", "", "repo root (default: current directory)")

	resolveRepo := func() string {
		if repo != "" {
			return repo
		}
		wd, _ := os.Getwd()
		return wd
	}

	indexCmd := &cobra.Command{
		Use:         "index",
		Short:       "Build or refresh the repo's graph",
		Args:        cobra.NoArgs,
		Annotations: bypass,
		RunE: func(cmd *cobra.Command, _ []string) error {
			gm, err := graphManager()
			if err != nil {
				return err
			}
			defer gm.Close()
			resp, err := gm.Index(cmd.Context(), resolveRepo())
			if err != nil {
				writeError("graph_index_failed", err.Error(), true)
				exitWith(ExitError)
			}
			writeJSON(resp)
			return nil
		},
	}

	var direction, to, symbolFlag string
	var depth int
	symbolCmd := &cobra.Command{
		Use:   "symbol <name>",
		Short: "Show a symbol's source + callers/callees as signature stubs",
		// Accept the target positionally OR via --symbol, so the MCP graph_symbol
		// arg name works verbatim on the CLI (surface parity).
		Args:        cobra.MaximumNArgs(1),
		Annotations: bypass,
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := graphTarget(args, symbolFlag, "symbol")
			if err != nil {
				return err
			}
			gm, err := graphManager()
			if err != nil {
				return err
			}
			defer gm.Close()
			resp, err := gm.Symbol(cmd.Context(), graph.SymbolRequest{
				Repo:      resolveRepo(),
				Symbol:    target,
				Direction: direction,
				Depth:     depth,
				To:        to,
			})
			if err != nil {
				writeGraphErr(err)
				return nil
			}
			writeString(graph.RenderSymbol(resp))
			return nil
		},
	}
	symbolCmd.Flags().StringVar(&direction, "direction", "both", "edges to follow: up | down | both")
	symbolCmd.Flags().IntVar(&depth, "depth", 1, "transitive hops (max 5); up + depth>1 = blast radius")
	symbolCmd.Flags().StringVar(&to, "to", "", "target symbol: return the call path instead of neighbors")
	symbolCmd.Flags().StringVar(&symbolFlag, "symbol", "", "symbol name (alias for the positional arg; matches MCP graph_symbol)")

	var packageFlag string
	packageCmd := &cobra.Command{
		Use:         "package <path>",
		Short:       "Show a package's exported surface (signatures only)",
		Args:        cobra.MaximumNArgs(1),
		Annotations: bypass,
		RunE: func(cmd *cobra.Command, args []string) error {
			target, err := graphTarget(args, packageFlag, "package")
			if err != nil {
				return err
			}
			gm, err := graphManager()
			if err != nil {
				return err
			}
			defer gm.Close()
			resp, err := gm.Package(cmd.Context(), graph.PackageRequest{
				Repo:    resolveRepo(),
				Package: target,
			})
			if err != nil {
				writeGraphErr(err)
				return nil
			}
			writeString(graph.RenderPackage(resp))
			return nil
		},
	}
	packageCmd.Flags().StringVar(&packageFlag, "package", "", "package path (alias for the positional arg; matches MCP graph_package)")

	cmd.AddCommand(indexCmd, symbolCmd, packageCmd)
	return cmd
}

// writeGraphErr emits the error envelope and exits (3 for misses, 1 otherwise).
func writeGraphErr(err error) {
	if errors.Is(err, graph.ErrNotFound) {
		writeError("not_found", err.Error(), true,
			withSuggestion("check spelling, or run `graph package <pkg>` to list symbols"))
		exitWith(ExitNotFound)
	}
	writeError("graph_query_failed", err.Error(), false)
	exitWith(ExitError)
}
