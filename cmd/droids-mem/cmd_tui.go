package main

import (
	"context"
	"encoding/json"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/samuelmolero26/droids-mem/internal/graph"
	"github.com/samuelmolero26/droids-mem/internal/tui"
	"github.com/spf13/cobra"
)

// graphAdapter wraps *graph.Manager to satisfy tui.GraphQuerier. It stores
// the repo root at construction time so the TUI never needs to ask for one.
type graphAdapter struct {
	gm   *graph.Manager
	repo string
}

func (a *graphAdapter) Package(ctx context.Context, repo, pkg string) (string, error) {
	r := a.repo
	if repo != "" {
		r = repo
	}
	resp, err := a.gm.Package(ctx, graph.PackageRequest{Repo: r, Package: pkg})
	if err != nil {
		return "", err
	}
	return marshalJSON(resp)
}

func (a *graphAdapter) Symbol(ctx context.Context, repo, symbol string) (string, error) {
	r := a.repo
	if repo != "" {
		r = repo
	}
	resp, err := a.gm.Symbol(ctx, graph.SymbolRequest{Repo: r, Symbol: symbol})
	if err != nil {
		return "", err
	}
	return marshalJSON(resp)
}

func newTUICmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Interactive terminal browser for the local memory corpus",
		Long: `Launches the Memory inspector: a three-pane browser (KINDS/SCOPE sidebar,
list, detail). Type to live-search (≥3 chars); tab cycles pane focus; arrows act
on the focused pane (kind filter / list / detail scroll); the detail pane follows
the list cursor. enter jumps to the detail pane, ctrl+d deletes with
confirmation, ctrl+g opens the graph tab (query call graph by symbol or
package), esc backs out or quits.

Sharing (ADR-0028): s cycles the SCOPE filter (all/personal/shared); space
multi-selects rows; ctrl+s opens the share-confirm dialog (flips the selection
into the git-tracked shared pool); ctrl+x unshares the cursor row. s and space
act only from an empty search box. Reads do not move the Expand signal; deletes
route through prune.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.store()
			if err != nil {
				return err
			}
			// Build optional graph manager from the current working directory.
			var gq tui.GraphQuerier
			gm, err := graphManager()
			if err == nil {
				wd, _ := os.Getwd()
				gq = &graphAdapter{gm: gm, repo: wd}
			}
			p := tea.NewProgram(tui.New(s, gq, ""), tea.WithAltScreen())
			_, err = p.Run()
			if gq != nil {
				gm.Close()
			}
			return err
		},
	}
}

// marshalJSON indents a value for the graph tab viewport.
func marshalJSON(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
