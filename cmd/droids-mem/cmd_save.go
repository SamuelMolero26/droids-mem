package main

import (
	"github.com/samuelmolero/droids-mem/internal/store"
	"github.com/spf13/cobra"
)

func newSaveCmd(s *store.Store) *cobra.Command {
	var (
		sessionID string
		taskType  string
		kind      string
		title     string
		what      string
		learned   string
		tags      string
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
			req := store.SaveRequest{
				SessionID: sessionID,
				TaskType:  taskType,
				Kind:      kind,
				Title:     title,
				What:      what,
				Learned:   learned,
				Tags:      tags,
				Force:     force,
			}

			if dryRun {
				if err := previewSave(s, req); err != nil {
					return err
				}
				return nil
			}

			resp, err := s.Save(req)
			if err != nil {
				if ve, ok := err.(*store.ValidationError); ok {
					writeError("validation_failed", ve.Message, false,
						withField(ve.Field),
						withInput(map[string]string{ve.Field: taskType}),
						withSuggestion("check --"+ve.Field+" value"),
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
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite existing memory with same fingerprint (HITL correction)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview save without writing to DB (exit 10 on pass)")

	cmd.MarkFlagRequired("task-type")
	cmd.MarkFlagRequired("kind")
	cmd.MarkFlagRequired("title")
	cmd.MarkFlagRequired("what")
	cmd.MarkFlagRequired("learned")

	return cmd
}

func previewSave(s *store.Store, req store.SaveRequest) error {
	// validate only — no insert
	req.Force = false
	resp, err := s.Save(store.SaveRequest{
		SessionID: req.SessionID,
		TaskType:  req.TaskType,
		Kind:      req.Kind,
		Title:     req.Title,
		What:      req.What,
		Learned:   req.Learned,
		Tags:      req.Tags,
		Force:     false,
	})
	if err != nil {
		if ve, ok := err.(*store.ValidationError); ok {
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
