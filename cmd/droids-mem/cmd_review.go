package main

import (
	"github.com/spf13/cobra"
)

func newReviewCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "review",
		Short: "Audit and reset the decay clock on memories (ADR-0031)",
		Long: `Memories of a decaying kind (error_resolution, task_pattern, user_rule)
carry a review_after clock; once it passes they surface as needs_review on
retrieval — a flag to re-confirm, never a filter (audit-only). This operator
workflow lets a human find and reset those.`,
	}
	cmd.AddCommand(newReviewListCmd(a), newReviewMarkReviewedCmd(a))
	return cmd
}

func newReviewListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List memories whose review_after has passed (the needs_review set)",
		Example: `  droids-mem review list`,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.store()
			if err != nil {
				return err
			}
			resp, err := s.ReviewList(cmd.Context())
			if err != nil {
				writeError("review_list_failed", err.Error(), true)
				exitWith(ExitError)
			}
			writeJSON(resp)
			return nil
		},
	}
}

func newReviewMarkReviewedCmd(a *app) *cobra.Command {
	var id string
	cmd := &cobra.Command{
		Use:   "mark-reviewed",
		Short: "Reset a memory's review_after clock without editing its body",
		Long: `Resets review_after to now + the kind's decay horizon — a metadata-only
write that leaves the body and updated_at untouched. Rejected (exit 2) for an
exempt kind (session_summary), which has no decay clock to reset.`,
		Example: `  droids-mem review mark-reviewed --id mem_01J9KXVR2E...`,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.store()
			if err != nil {
				return err
			}
			mem, err := s.MarkReviewed(cmd.Context(), id)
			if err != nil {
				writeLifecycleMutationError("mark_reviewed_failed", err)
			}
			if mem == nil {
				writeLifecycleNotFound(id)
			}
			writeJSON(mem)
			return nil
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "Memory ID with mem_ prefix (required)")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}
