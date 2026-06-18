package main

import (
	"errors"

	"strings"

	"github.com/samuelmolero26/droids-mem/internal/store"
	"github.com/spf13/cobra"
)

func newSaveCmd(a *app) *cobra.Command {
	var (
		sessionID string
		taskType  string
		kind      string
		title     string
		what      string
		learned   string
		tags      string
		scope     string
		force     bool
		dryRun    bool
	)

	cmd := &cobra.Command{
		Use:   "save",
		Short: "Save a structured memory",
		Example: `  droids-mem save --task-type crm_upload --kind error_resolution \
    --title "HubSpot phone field" --what "field was phone_number" \
    --learned "map to phone, not phone_number" --tags "hubspot phone"

  # HITL correction — overwrite existing memory
  droids-mem save --task-type crm_upload --kind user_rule \
    --title "Company abbreviation" --what "corrected" --learned "use Co. not Company" --force

  # Preview without writing
  droids-mem save --task-type crm_upload --kind error_resolution \
    --title "..." --what "..." --learned "..." --dry-run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.store()
			if err != nil {
				return err
			}
			req := store.SaveRequest{
				SessionID: sessionID,
				TaskType:  taskType,
				Kind:      kind,
				Title:     title,
				What:      what,
				Learned:   learned,
				Tags:      tags,
				Scope:     scope,
				Force:     force,
			}

			if dryRun {
				return previewSave(cmd, s, req)
			}

			resp, err := s.Save(cmd.Context(), req)
			if err != nil {
				var ve *store.ValidationError
				if errors.As(err, &ve) {
					fieldVals := map[string]string{
						"session_id": sessionID, "task_type": taskType,
						"kind": kind, "title": title, "what": what,
						"learned": learned, "tags": tags,
					}
					flag := strings.ReplaceAll(ve.Field, "_", "-")
					writeError("validation_failed", ve.Message, false,
						withField(ve.Field),
						withInput(map[string]string{ve.Field: fieldVals[ve.Field]}),
						withSuggestion("check --"+flag+" value"),
					)
					exitWith(ExitUsage)
				}
				writeError("save_failed", err.Error(), true)
				exitWith(ExitError)
			}

			writeJSON(resp)
			if resp.Status == "skipped" {
				exitWith(ExitConflict)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&sessionID, "session-id", "", "Session ID to group memories (auto-generated if omitted)")
	cmd.Flags().StringVar(&taskType, "task-type", "", "Task type identifier, e.g. crm_upload (required)")
	cmd.Flags().StringVar(&kind, "kind", "", "Memory kind: error_resolution|task_pattern|user_rule|session_summary (required)")
	cmd.Flags().StringVar(&title, "title", "", "Short title for the memory (required)")
	cmd.Flags().StringVar(&what, "what", "", "What happened (required)")
	cmd.Flags().StringVar(&learned, "learned", "", "What the agent should do next time (required)")
	cmd.Flags().StringVar(&tags, "tags", "", "Space-delimited tags, e.g. \"hubspot phone field-mapping\"")
	cmd.Flags().StringVar(&scope, "scope", "", "Memory scope: shared (default) | personal")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite existing memory with same fingerprint (HITL correction)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview save without writing to DB (exit 10 on pass)")

	_ = cmd.MarkFlagRequired("task-type")
	_ = cmd.MarkFlagRequired("kind")
	_ = cmd.MarkFlagRequired("title")
	_ = cmd.MarkFlagRequired("what")
	_ = cmd.MarkFlagRequired("learned")

	return cmd
}

func previewSave(cmd *cobra.Command, s *store.Store, req store.SaveRequest) error {
	// DryRun runs the full save pipeline (validate → scrub → dedupe) under the
	// real write lock, then rolls back — nothing persists.
	req.Force = false
	req.DryRun = true
	resp, err := s.Save(cmd.Context(), req)
	if err != nil {
		var ve *store.ValidationError
		if errors.As(err, &ve) {
			writeError("validation_failed", ve.Message, false, withField(ve.Field))
			exitWith(ExitUsage)
		}
		writeError("save_failed", err.Error(), true)
		exitWith(ExitError)
	}
	// dry-run: report what would happen without committing
	writeJSON(map[string]any{
		"status":     "dry_run",
		"would":      resp.Status,
		"matched_id": resp.MatchedID,
		"reason":     resp.Reason,
	})
	exitWith(ExitDryRun)
	return nil
}
