package main

import (
	"errors"

	"github.com/samuelmolero26/droids-mem/internal/store"
	"github.com/spf13/cobra"
)

func newPinCmd(a *app) *cobra.Command {
	var id string
	cmd := &cobra.Command{
		Use:   "pin",
		Short: "Pin a memory to the top of its task_type's context bundle",
		Long: `Pin marks a memory so it always leads the always-tier of mem_context for
its task_type (ADR-0031), outside the ordinary caps and chronological order.

Capped at 5 pins per task_type; a 6th is rejected (exit 5) — unpin one first.
Re-pinning an already-pinned memory is a no-op (exit 0). Curation is a human
act: pin is CLI-only and never exposed to agents over MCP.`,
		Example: `  droids-mem pin --id mem_01J9KXVR2E...`,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.store()
			if err != nil {
				return err
			}
			mem, err := s.Pin(cmd.Context(), id)
			if err != nil {
				writeLifecycleMutationError("pin_failed", err)
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

func newUnpinCmd(a *app) *cobra.Command {
	var id string
	cmd := &cobra.Command{
		Use:     "unpin",
		Short:   "Remove a memory's pin",
		Example: `  droids-mem unpin --id mem_01J9KXVR2E...`,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.store()
			if err != nil {
				return err
			}
			mem, err := s.Unpin(cmd.Context(), id)
			if err != nil {
				writeLifecycleMutationError("unpin_failed", err)
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

// writeLifecycleMutationError maps a store lifecycle-mutation error to the CLI
// exit contract: pin_cap_exceeded → exit 5 (conflict), any other
// ValidationError → exit 2 (usage), everything else → exit 1.
func writeLifecycleMutationError(failCode string, err error) {
	var ve *store.ValidationError
	if errors.As(err, &ve) {
		code := ve.Code
		if code == "" {
			code = "validation_failed"
		}
		writeError(code, ve.Message, ve.Retryable, withField(ve.Field), withSuggestion(ve.Suggestion))
		if ve.Code == "pin_cap_exceeded" {
			exitWith(ExitConflict)
		}
		exitWith(ExitUsage)
	}
	writeError(failCode, err.Error(), true)
	exitWith(ExitError)
}

func writeLifecycleNotFound(id string) {
	writeError("not_found", "no memory with id "+id, false,
		withField("id"),
		withInput(map[string]string{"id": id}),
		withSuggestion("use 'droids-mem list' to find valid IDs"),
	)
	exitWith(ExitNotFound)
}
