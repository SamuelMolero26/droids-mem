package main

import (
	"errors"

	"github.com/SamuelMolero26/droids-mem/internal/store"
	"github.com/spf13/cobra"
)

func newPruneCmd(a *app) *cobra.Command {
	var (
		id            string
		kind          string
		taskType      string
		olderThanDays int
		apply         bool
		suggestDupes  bool
		threshold     float64
	)
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Manually delete memories, or discover likely-duplicate clusters",
		Long: `Manual retention tool (ADR-0010). The system never deletes knowledge
memories on its own; prune is the explicit, human-initiated path.

Default mode is a dry run: it prints the rows that WOULD be deleted and exits
with code 10. Pass --apply to actually delete. At least one of --id, --kind,
--task-type, or --older-than-days is required — pruning the entire database is
refused. --id deletes exactly one memory by id (the other filters are ignored).

Pass --suggest-dupes to instead scan for clusters of likely-duplicate
memories (FTS5 BM25 candidates verified by Jaccard similarity at a relaxed
threshold, default 0.6). Read-only: review the clusters, then prune by id
or consolidate manually.`,
		Example: `  droids-mem prune --kind error_resolution --older-than-days 180
  droids-mem prune --task-type crm_upload --apply
  droids-mem prune --suggest-dupes
  droids-mem prune --suggest-dupes --task-type crm_upload --threshold 0.7`,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.store()
			if err != nil {
				return err
			}
			ctx := cmd.Context()

			if suggestDupes {
				if apply {
					writeError("invalid_arguments", "--suggest-dupes is read-only and cannot be combined with --apply", true)
					exitWith(ExitUsage)
				}
				resp, err := s.SuggestDupes(ctx, store.SuggestDupesRequest{
					Kind: kind, TaskType: taskType, Threshold: threshold,
				})
				if err != nil {
					writePruneError(err)
				}
				writeJSON(resp)
				return nil
			}

			resp, err := s.Prune(ctx, store.PruneRequest{
				ID: id, Kind: kind, TaskType: taskType, OlderThanDays: olderThanDays, Apply: apply,
			})
			if err != nil {
				writePruneError(err)
			}
			writeJSON(resp)
			if !apply {
				exitWith(ExitDryRun)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "Delete exactly this memory by id (other filters ignored).")
	cmd.Flags().StringVar(&kind, "kind", "", "Only memories of this kind.")
	cmd.Flags().StringVar(&taskType, "task-type", "", "Only memories of this task_type.")
	cmd.Flags().IntVar(&olderThanDays, "older-than-days", 0, "Only memories created more than N days ago.")
	cmd.Flags().BoolVar(&apply, "apply", false, "Actually delete. Without it prune is a dry run (exit 10).")
	cmd.Flags().BoolVar(&suggestDupes, "suggest-dupes", false, "Emit likely-duplicate clusters instead of deleting.")
	cmd.Flags().Float64Var(&threshold, "threshold", store.DefaultSuggestThreshold, "Jaccard threshold for --suggest-dupes, in (0,1].")
	return cmd
}

func writePruneError(err error) {
	var ve *store.ValidationError
	if errors.As(err, &ve) {
		writeError(ve.Code, ve.Message, ve.Retryable, withField(ve.Field), withSuggestion(ve.Suggestion))
		exitWith(ExitUsage)
	}
	writeError("prune_failed", err.Error(), true)
	exitWith(ExitError)
}
