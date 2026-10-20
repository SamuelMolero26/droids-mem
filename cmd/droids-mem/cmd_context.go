package main

import (
	"errors"

	"github.com/samuelmolero26/droids-mem/internal/store"
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
		Args:  cobra.NoArgs,
		Long: `Returns a byte-bounded two-tier bundle:
  - always tier: latest session_summary + highest-priority user_rules
  - browse tier: rule stubs + top error_resolution/task_pattern memories

Agent should call 'droids-mem get --id ...' or 'droids-mem search ...' to
recover stubbed or omitted detail.`,
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
				if ve, ok := errors.AsType[*store.ValidationError](err); ok {
					failValidation(ve, "provide --"+ve.Field)
				}
				writeError("context_failed", err.Error(), true)
				exitWith(ExitError)
			}
			if resp.StubbedCount > 0 {
				resp.Help = []string{"Run 'droids-mem get --id <id>' to read a returned stub in full"}
			}
			if resp.OmittedCount > 0 {
				resp.Help = append(resp.Help, "Run 'droids-mem search --query <terms>', then 'droids-mem get --id <id>' to recover omitted memories")
			}
			writeBoundedJSON(resp, store.MaxContextResponseBytes)
			return nil
		},
	}

	cmd.Flags().StringVar(&taskType, "task-type", "", "Task type to load context for (required)")
	cmd.Flags().StringVar(&query, "query", "", "Optional FTS query for browse-tier ranking; absent or punctuation-only ranks the tier by recency instead")
	cmd.Flags().StringVar(&mode, "mode", "orient", "Retrieval depth: orient | deep | refresh")

	_ = cmd.MarkFlagRequired("task-type")

	return cmd
}
