package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/samuelmolero26/droids-mem/internal/state"
)

// ponytail: fixed window, no --within flag until someone wants a different one.
const graphBadgeWindow = 60 * time.Second

func newStatuslineCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "statusline",
		Short: "Print a status-line badge when the code graph was just queried",
		Long: `Prints "droids-mem:<tool>" if a graph tool ran in the last 60s, else nothing.
Wire into ~/.claude/settings.json:

  { "statusLine": { "type": "command", "command": "droids-mem statusline" } }`,
		Args: cobra.NoArgs,
		// No DB needed: the marker is a file in the state dir.
		Annotations: map[string]string{bootGateBypass: "true"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			if tool, ok := state.LastGraphUse(graphBadgeWindow); ok {
				fmt.Fprintf(cmd.OutOrStdout(), "droids-mem:%s\n", tool)
			}
			return nil
		},
	}
}
