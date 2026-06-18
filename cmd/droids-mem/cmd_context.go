package main

import (
	"errors"

	"github.com/SamuelMolero26/droids-mem/internal/store"
	"github.com/spf13/cobra"
)

func newContextCmd(a *app) *cobra.Command {
	var (
		taskType string
		query    string
		mode     string
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
			s, err := a.store()
			if err != nil {
				return err
			}
			resp, err := s.Context(cmd.Context(), store.ContextRequest{
				TaskType: taskType,
				Query:    query,
				Mode:     store.ContextMode(mode),
			})
			if err != nil {
				var ve *store.ValidationError
				if errors.As(err, &ve) {
					suggestion := ve.Suggestion
					if suggestion == "" {
						suggestion = "provide --" + ve.Field
					}
					writeError("validation_failed", ve.Message, false,
						withField(ve.Field),
						withSuggestion(suggestion),
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
	cmd.Flags().StringVar(&mode, "mode", "orient", "Retrieval depth: orient | deep | refresh")

	_ = cmd.MarkFlagRequired("task-type")

	return cmd
}
