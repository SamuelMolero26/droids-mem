package main

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/samuelmolero26/droids-mem/internal/graph"
	"github.com/samuelmolero26/droids-mem/internal/state"
)

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

	var direction, to string
	var depth int
	symbolCmd := &cobra.Command{
		Use:         "symbol <name>",
		Short:       "Show a symbol's source + callers/callees as signature stubs",
		Args:        cobra.ExactArgs(1),
		Annotations: bypass,
		RunE: func(cmd *cobra.Command, args []string) error {
			gm, err := graphManager()
			if err != nil {
				return err
			}
			defer gm.Close()
			resp, err := gm.Symbol(cmd.Context(), graph.SymbolRequest{
				Repo:      resolveRepo(),
				Symbol:    args[0],
				Direction: direction,
				Depth:     depth,
				To:        to,
			})
			if err != nil {
				writeGraphErr(err)
				return nil
			}
			writeJSON(resp)
			return nil
		},
	}
	symbolCmd.Flags().StringVar(&direction, "direction", "both", "edges to follow: up | down | both")
	symbolCmd.Flags().IntVar(&depth, "depth", 1, "transitive hops (max 5); up + depth>1 = blast radius")
	symbolCmd.Flags().StringVar(&to, "to", "", "target symbol: return the call path instead of neighbors")

	packageCmd := &cobra.Command{
		Use:         "package <path>",
		Short:       "Show a package's exported surface (signatures only)",
		Args:        cobra.ExactArgs(1),
		Annotations: bypass,
		RunE: func(cmd *cobra.Command, args []string) error {
			gm, err := graphManager()
			if err != nil {
				return err
			}
			defer gm.Close()
			resp, err := gm.Package(cmd.Context(), graph.PackageRequest{
				Repo:    resolveRepo(),
				Package: args[0],
			})
			if err != nil {
				writeGraphErr(err)
				return nil
			}
			writeJSON(resp)
			return nil
		},
	}

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
