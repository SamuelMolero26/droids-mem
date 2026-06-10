package main

import (
	"errors"

	"github.com/samuelmolero/droids-mem/internal/store"
	"github.com/spf13/cobra"
)

func newGetCmd(a *app) *cobra.Command {
	var id string

	cmd := &cobra.Command{
		Use:     "get",
		Short:   "Get a single memory by ID",
		Example: `  droids-mem get --id mem_01J9KXVR2E...`,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.store()
			if err != nil {
				return err
			}
			mem, err := s.Get(cmd.Context(), id)
			if err != nil {
				var ve *store.ValidationError
				if errors.As(err, &ve) {
					writeError("validation_failed", ve.Message, false,
						withField(ve.Field),
						withSuggestion("provide --id with a valid mem_ prefixed ID"),
					)
					exitWith(ExitUsage)
				}
				writeError("get_failed", err.Error(), true)
				exitWith(ExitError)
			}
			if mem == nil {
				writeError("not_found", "no memory with id "+id, false,
					withField("id"),
					withInput(map[string]string{"id": id}),
					withSuggestion("use 'droids-mem list' to find valid IDs"),
				)
				exitWith(ExitNotFound)
			}
			writeJSON(mem)
			return nil
		},
	}

	cmd.Flags().StringVar(&id, "id", "", "Memory ID with mem_ prefix (required)")
	_ = cmd.MarkFlagRequired("id")

	return cmd
}
