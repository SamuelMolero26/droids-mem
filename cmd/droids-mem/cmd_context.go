package main

import (
	"github.com/samuelmolero/droids-mem/internal/store"
	"github.com/spf13/cobra"
)

func newContextCmd(s *store.Store) *cobra.Command {
	var (
		taskType string
		query    string
	)

	cmd := &cobra.Command{
		Use:   "context",
		Short: "Load start-of-run context bundle for a task type",
		Long: `Returns a two-tier bundle:
  - always tier: latest session_summary + ALL user_rules (full body)
  - browse tier: top error_resolution + task_pattern (title + snippet)

Agent should call 'droids-mem get --id ...' or 'droids-mem search ...' to
deep-read any browse-tier item.`,
		Example: `  droids-mem context --task-type crm_upload
  droids-mem context --task-type crm_upload --query "hubspot phone field mapping"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := s.Context(store.ContextRequest{
				TaskType: taskType,
				Query:    query,
			})
			if err != nil {
				if ve, ok := err.(*store.ValidationError); ok {
					writeError("validation_failed", ve.Message, false,
						withField(ve.Field),
						withSuggestion("provide --"+ve.Field),
					)
					exitWith(ExitUsage)
				}
				writeError("context_failed", err.Error(), true)
				exitWith(ExitError)
			}
			writeJSON(resp)
			return nil
		},
	}

	cmd.Flags().StringVar(&taskType, "task-type", "", "Task type to load context for (required)")
	cmd.Flags().StringVar(&query, "query", "", "Optional FTS query for browse-tier ranking (defaults to task-type tokens)")

	cmd.MarkFlagRequired("task-type")

	return cmd
}
