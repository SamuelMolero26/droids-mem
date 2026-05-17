package main

import (
	"github.com/samuelmolero/droids-mem/internal/db"
	"github.com/samuelmolero/droids-mem/internal/store"
	"github.com/spf13/cobra"
)

func newDoctorCmd(s *store.Store) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check FTS integrity, rebuild if divergent, optimize, and VACUUM",
		Long: `Diagnostic + recovery tool for the SQLite store.

Runs in this order:
  1. FTS integrity-check (detects divergence between memories and memories_fts)
  2. Rebuild FTS index if integrity-check failed
  3. Optimize FTS index (merge segments)
  4. VACUUM (reclaim free pages in the DB file)

Safe to run at any time. Reports bytes freed and whether a rebuild occurred.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rep, err := s.Doctor(db.ResolvePath())
			if err != nil {
				writeError("doctor_failed", err.Error(), true)
				exitWith(ExitError)
			}
			writeJSON(rep)
			return nil
		},
	}
}
