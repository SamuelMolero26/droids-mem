package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"

	"github.com/samuelmolero26/droids-mem/internal/db"
	"github.com/samuelmolero26/droids-mem/internal/store"
	"github.com/spf13/cobra"
)

// version is the build version, injected via -ldflags "-X main.version=...".
// Defaults to "dev" for local builds.
var version = "dev"

// bootGateBypass marks a command as exempt from db.AssertBootReady. Used by
// the migrate subcommand, which exists precisely to satisfy the gate, and by
// `schema` which only prints DDL without touching live data.
const bootGateBypass = "bypass_boot_gate"

// app lazily opens the database on first use so commands that never touch
// data (--help, schema, bare flag errors) leave the filesystem alone — no
// state dir, no DB file, no migrations as a side effect of printing help.
type app struct {
	once sync.Once
	db   *sql.DB
	st   *store.Store
	err  error
}

// store opens the DB on first call and memoizes the result. Safe to call
// from PersistentPreRunE and RunE in any order.
func (a *app) store() (*store.Store, error) {
	a.once.Do(func() {
		a.db, a.err = db.Open()
		if a.err == nil {
			a.st = store.New(a.db)
		}
	})
	if a.err != nil {
		return nil, &dbInitError{a.err}
	}
	return a.st, nil
}

func (a *app) close() {
	if a.db != nil {
		_ = a.db.Close()
	}
}

// dbInitError tags failures from db.Open so main can emit the db_init_failed
// envelope instead of a generic usage error.
type dbInitError struct{ err error }

func (e *dbInitError) Error() string { return e.err.Error() }
func (e *dbInitError) Unwrap() error { return e.err }

func main() {
	a := &app{}
	defer a.close()

	root := &cobra.Command{
		Use:     "droids-mem",
		Version: version,
		Short:   "Persistent memory tool for AI agents",
		Long: `droids-mem gives AI agents a persistent memory layer backed by SQLite + FTS5.

Agents save structured lessons, search past lessons, and load context at the
start of each run — all via a local binary with zero external dependencies.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Annotations[bootGateBypass] == "true" {
				return nil
			}
			s, err := a.store()
			if err != nil {
				return err
			}
			err = db.AssertBootReady(a.db)
			if err == nil || !db.IsBootGateError(err) {
				return err // ready, or a failure migrating can't fix — surface as-is
			}
			// Auto-remediate rather than take down all memory tools until a
			// manual migrate (issue #29). --rescrub is the only safe unattended
			// mode: it re-runs the scrub patterns, so it can only make data
			// more-scrubbed. --no-rescrub encodes the human judgment "no row
			// holds plaintext" and must never be auto-chosen.
			// Runs for every non-bypassed command, so the first read (list,
			// search, doctor) after an upgrade triggers this one-time write;
			// on a large corpus it can outlast ensure-server's 5s /healthz poll,
			// so the spawned serve may briefly report "not healthy" mid-migration.
			// ponytail: concurrent cold-start (hook → ensure-server → serve) can
			// race here; Migrate's BEGIN IMMEDIATE serializes them and losers
			// re-rescrub idempotently. Add coordination only if that waste bites.
			summary, merr := store.Migrate(s, store.MigrateOptions{Rescrub: true})
			if merr != nil {
				// Fail closed: return the gate error so the manual remediation
				// string still shows; log merr since the gate error omits it.
				log.Printf("boot gate: auto-migration failed: %v", merr)
				return err
			}
			log.Printf("boot gate: auto-rescrubbed stale DB — %d rows, %d redactions",
				summary.RowsRewritten, summary.TotalRedactions)
			return nil
		},
	}

	root.AddCommand(
		newSaveCmd(a),
		newSearchCmd(a),
		newContextCmd(a),
		newListCmd(a),
		newRecentSessionsCmd(a),
		newSessionCmd(a),
		newInstallCmd(),
		newUninstallCmd(),
		newGetCmd(a),
		newDoctorCmd(a),
		newPruneCmd(a),
		newSchemaCmd(),
		newScrubCmd(),
		newTUICmd(a),
		newGraphCmd(),
		newServeCmd(a),
		newEnsureServerCmd(),
		newMigrateCmd(a),
		newShareCmd(a),
		newUnshareCmd(a),
		newExportCmd(a),
		newImportCmd(a),
	)

	if err := root.Execute(); err != nil {
		var initErr *dbInitError
		if errors.As(err, &initErr) {
			writeError("db_init_failed", initErr.Error(), false,
				withSuggestion("check DROIDS_MEM_DB env var or ensure ~/.droids-mem/ is writable"),
			)
			os.Exit(ExitError)
		}
		var bg *db.BootGateError
		if errors.As(err, &bg) {
			writeError("boot_gate", bg.Reason, false,
				withSuggestion(bg.Migration),
			)
			os.Exit(ExitError)
		}
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(ExitUsage)
	}
}

// exitWith terminates the process with the given exit code.
// Used by command handlers after writing their output.
func exitWith(code int) {
	os.Exit(code)
}
