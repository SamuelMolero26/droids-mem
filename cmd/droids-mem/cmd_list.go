package main

import (
	"errors"

	"github.com/samuelmolero/droids-mem/internal/store"
	"github.com/spf13/cobra"
)

func newListCmd(a *app) *cobra.Command {
	var (
		taskType string
		kind     string
		limit    int
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List recent memories",
		Example: `  droids-mem list
  droids-mem list --task-type crm_upload
  droids-mem list --kind error_resolution --limit 10`,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.store()
			if err != nil {
				return err
			}
			resp, err := s.List(cmd.Context(), store.ListRequest{
				TaskType: taskType,
				Kind:     kind,
				Limit:    limit,
			})
			if err != nil {
				var ve *store.ValidationError
				if errors.As(err, &ve) {
					writeError("validation_failed", ve.Message, false,
						withField(ve.Field),
						withSuggestion("check --"+ve.Field+" value"),
					)
					exitWith(ExitUsage)
				}
				writeError("list_failed", err.Error(), true)
				exitWith(ExitError)
			}
			writeJSON(resp)
			return nil
		},
	}

	cmd.Flags().StringVar(&taskType, "task-type", "", "Filter by task type")
	cmd.Flags().StringVar(&kind, "kind", "", "Filter by kind: error_resolution|task_pattern|user_rule|session_summary")
	cmd.Flags().IntVar(&limit, "limit", 20, "Max results (default 20, max 100)")

	return cmd
}
