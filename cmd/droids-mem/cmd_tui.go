package main

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/samuelmolero26/droids-mem/internal/tui"
	"github.com/spf13/cobra"
)

func newTUICmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Interactive terminal browser for the local memory corpus",
		Long: `Launches the Memory inspector: a three-pane browser (KINDS/SCOPE sidebar,
list, detail). Type to live-search (≥3 chars); tab cycles pane focus; arrows act
on the focused pane (kind filter / list / detail scroll); the detail pane follows
the list cursor. enter jumps to the detail pane, ctrl+d deletes with
confirmation, esc backs out or quits.

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
			p := tea.NewProgram(tui.New(s), tea.WithAltScreen())
			_, err = p.Run()
			return err
		},
	}
}
