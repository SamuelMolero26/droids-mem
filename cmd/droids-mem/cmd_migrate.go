package main

import (
	"errors"

	"github.com/samuelmolero26/droids-mem/internal/store"
	"github.com/spf13/cobra"
)

func newMigrateCmd(a *app) *cobra.Command {
	var (
		rescrub   bool
		noRescrub bool
	)
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Establish the v1.0 scrub baseline on an existing database",
		Long: `migrate transitions a pre-v1.0 database to the v1.0 scrub-aware schema.

It runs in one of two modes (exactly one is required):

  --rescrub     Walk every row, re-run the scrub patterns against title/what/
                learned, and refresh the fingerprint. Atomic per DB — partial
                failure leaves the database on the prior shape.

  --no-rescrub  Acknowledge that existing rows are NOT scrubbed and mark the
                database baseline-complete anyway. Use only when the operator
                has independently confirmed that no row holds plaintext
                secrets.

Both modes set meta.scrub_baseline_complete='1', which the boot gate checks
before every other subcommand will start.

The porter-stemmer tokenizer flip is NOT part of this command: it happens
automatically via the boot ladder (rung 6→7) on first open after an upgrade.
migrate only rewrites rows (--rescrub) and stamps the sentinel.`,
		Example: `  # Standard upgrade path — rewrites every row through the scrub patterns.
  droids-mem migrate --rescrub

  # Operator has verified the corpus is clean; skip the rewrite.
  droids-mem migrate --no-rescrub`,
		Annotations: map[string]string{bootGateBypass: "true"},
		RunE: func(_ *cobra.Command, _ []string) error {
			if rescrub == noRescrub {
				writeError("usage_error", "specify exactly one of --rescrub or --no-rescrub", false,
					withSuggestion("re-run with `droids-mem migrate --rescrub` (recommended) or `--no-rescrub`"),
				)
				exitWith(ExitUsage)
			}
			// migrate bypasses the boot gate, so it opens the DB itself here.
			s, err := a.store()
			if err != nil {
				return err
			}
			summary, err := store.Migrate(s, store.MigrateOptions{Rescrub: rescrub})
			if err != nil {
				// A re-fingerprint collision is an operator decision, not a
				// retryable runtime failure: exit 5 (conflict/duplicate class).
				var coll *store.FingerprintCollisionError
				if errors.As(err, &coll) {
					writeError("migrate_collision", err.Error(), false,
						withSuggestion("deduplicate the colliding rows (delete or merge one), then re-run 'droids-mem migrate --rescrub'"),
					)
					exitWith(ExitConflict)
				}
				writeError("migrate_failed", err.Error(), true)
				exitWith(ExitError)
			}
			writeJSON(summary)
			return nil
		},
	}
	cmd.Flags().BoolVar(&rescrub, "rescrub", false,
		"Rewrite every row with the current scrub patterns and refresh fingerprints (recommended).")
	cmd.Flags().BoolVar(&noRescrub, "no-rescrub", false,
		"Mark the baseline complete without rewriting rows; existing plaintext stays as-is.")
	return cmd
}
