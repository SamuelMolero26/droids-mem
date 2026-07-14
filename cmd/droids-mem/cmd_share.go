package main

import (
	"github.com/spf13/cobra"
)

// newShareCmd flips a memory to scope='shared' so `export` will include it.
// Sharing is a deliberate operator act across a trust boundary, so it lives on
// the CLI only — not the MCP surface (ADR-0028, consistent with prune/list).
func newShareCmd(a *app) *cobra.Command {
	return newSetScopeCmd(a, "share", "shared", "Mark a memory as shared (eligible for export)")
}

// newUnshareCmd reverts a memory to scope='personal', pulling it back out of
// the exportable pool.
func newUnshareCmd(a *app) *cobra.Command {
	return newSetScopeCmd(a, "unshare", "personal", "Mark a memory as personal (excluded from export)")
}

func newSetScopeCmd(a *app, use, scope, short string) *cobra.Command {
	var id string
	cmd := &cobra.Command{
		Use:     use,
		Short:   short,
		Example: "  droids-mem " + use + " --id mem_01J9KXVR2E...",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.store()
			if err != nil {
				return err
			}
			found, err := s.SetScope(cmd.Context(), id, scope)
			if err != nil {
				writeError(use+"_failed", err.Error(), true)
				exitWith(ExitError)
			}
			if !found {
				writeError("not_found", "no memory with id "+id, false,
					withField("id"),
					withSuggestion("use 'droids-mem list' to find valid IDs"),
				)
				exitWith(ExitNotFound)
			}
			writeJSON(map[string]string{"status": "ok", "id": id, "scope": scope})
			return nil
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "Memory ID with mem_ prefix (required)")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}

// newExportCmd streams every shared memory as JSONL to stdout (ADR-0028). Pipe
// it into a git-tracked file to share: `droids-mem export > team/shared.jsonl`.
func newExportCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "export",
		Short:   "Export shared memories as JSONL to stdout",
		Example: "  droids-mem export > team/shared.jsonl",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.store()
			if err != nil {
				return err
			}
			mems, err := s.ExportShared(cmd.Context())
			if err != nil {
				writeError("export_failed", err.Error(), true)
				exitWith(ExitError)
			}
			for _, m := range mems {
				writeJSON(m) // one compact JSON object per line
			}
			return nil
		},
	}
}

// newImportCmd reads a shared-pool JSONL stream from stdin and merges it in
// (ADR-0028). Every row goes through the normal save path: re-scrubbed, deduped
// (Jaccard≥0.85), retained. `droids-mem import < team/shared.jsonl`.
func newImportCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "import",
		Short:   "Import a shared-pool JSONL stream from stdin",
		Example: "  droids-mem import < team/shared.jsonl",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.store()
			if err != nil {
				return err
			}
			res, err := s.ImportShared(cmd.Context(), cmd.InOrStdin())
			if err != nil {
				writeError("import_failed", err.Error(), true)
				exitWith(ExitError)
			}
			writeJSON(res)
			return nil
		},
	}
}
