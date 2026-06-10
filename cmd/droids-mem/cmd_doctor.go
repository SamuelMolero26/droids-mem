package main

import (
	"github.com/samuelmolero/droids-mem/internal/db"
	"github.com/spf13/cobra"
)

func newDoctorCmd(a *app) *cobra.Command {
	var scrubStats bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check FTS integrity, rebuild if divergent, optimize, and VACUUM",
		Long: `Diagnostic + recovery tool for the SQLite store.

Default mode runs in this order:
  1. FTS integrity-check (detects divergence between memories and memories_fts)
  2. Rebuild FTS index if integrity-check failed
  3. Optimize FTS index (merge segments)
  4. VACUUM (reclaim free pages in the DB file)

Pass --scrub-stats to skip the FTS pipeline and instead emit an aggregate
report of scrub coverage across the corpus (rows_total, rows_with_redactions,
per_pattern counts, process-lifetime rejected-save counters).

Safe to run at any time.`,
		Example: `  droids-mem doctor
  droids-mem doctor --scrub-stats`,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.store()
			if err != nil {
				return err
			}
			if scrubStats {
				rep, err := s.ScrubStats()
				if err != nil {
					writeError("doctor_failed", err.Error(), true)
					exitWith(ExitError)
				}
				writeJSON(rep)
				return nil
			}
			rep, err := s.Doctor(db.ResolvePath())
			if err != nil {
				writeError("doctor_failed", err.Error(), true)
				exitWith(ExitError)
			}
			writeJSON(rep)
			return nil
		},
	}
	cmd.Flags().BoolVar(&scrubStats, "scrub-stats", false,
		"Emit an aggregate scrub-coverage report instead of running the FTS pipeline.")
	return cmd
}
