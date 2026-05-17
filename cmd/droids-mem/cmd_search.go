package main

import (
	"github.com/samuelmolero/droids-mem/internal/store"
	"github.com/spf13/cobra"
)

func newSearchCmd(s *store.Store) *cobra.Command {
	var (
		query    string
		taskType string
		kind     string
		limit    int
	)

	cmd := &cobra.Command{
		Use:   "search",
		Short: "Search memories using full-text search",
		Example: `  droids-mem search --query "hubspot phone mapping"
  droids-mem search --query "phone" --task-type crm_upload --kind error_resolution
  droids-mem search --query "auth failure" --limit 10`,
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := s.Search(store.SearchRequest{
				Query:    query,
				TaskType: taskType,
				Kind:     kind,
				Limit:    limit,
			})
			if err != nil {
				if ve, ok := err.(*store.ValidationError); ok {
					writeError("validation_failed", ve.Message, false,
						withField(ve.Field),
						withSuggestion("provide --"+ve.Field),
					)
					exitWith(ExitUsage)
				}
				writeError("search_failed", err.Error(), true)
				exitWith(ExitError)
			}
			writeJSON(resp)
			return nil
		},
	}

	cmd.Flags().StringVar(&query, "query", "", "Full-text search query (required)")
	cmd.Flags().StringVar(&taskType, "task-type", "", "Filter by task type, e.g. crm_upload")
	cmd.Flags().StringVar(&kind, "kind", "", "Filter by kind: error_resolution|task_pattern|user_rule|session_summary")
	cmd.Flags().IntVar(&limit, "limit", 5, "Max results to return (default 5, max 20)")

	cmd.MarkFlagRequired("query")

	return cmd
}
