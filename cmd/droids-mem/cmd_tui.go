package main

import (
	"github.com/SamuelMolero26/droids-mem/internal/tui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
)

func newTUICmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Interactive terminal browser for the local memory corpus",
		Long: `Launches the Memory inspector: browse, filter (tab cycles kind), live-search
(type ≥3 chars), open a memory full-screen (enter), and delete one with
confirmation (ctrl+d). Reads do not move the Expand signal; deletes route
through prune.`,
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
