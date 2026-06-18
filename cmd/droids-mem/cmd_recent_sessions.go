package main

import (
	"github.com/SamuelMolero26/droids-mem/internal/store"
	"github.com/spf13/cobra"
)

// newRecentSessionsCmd is the human/operator recency view over auto-session
// summaries (ADR-0016 pt 7). Intentionally CLI-only — not exposed over MCP. The
// agent reaches auto summaries through the relevance-gated mem_search/mem_get
// path; this command is for a person reviewing their own recent Claude Code runs.
func newRecentSessionsCmd(a *app) *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:   "recent-sessions",
		Short: "List recent auto-saved Claude Code session summaries (newest first)",
		Example: `  droids-mem recent-sessions
  droids-mem recent-sessions --limit 5`,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.store()
			if err != nil {
				return err
			}
			resp, err := s.RecentSessions(cmd.Context(), store.RecentSessionsRequest{
				Limit: limit,
			})
			if err != nil {
				writeError("recent_sessions_failed", err.Error(), true)
				exitWith(ExitError)
			}
			writeJSON(resp)
			return nil
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 10, "Max sessions (default 10, max 100)")

	return cmd
}
