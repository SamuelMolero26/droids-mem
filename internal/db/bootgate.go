package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// BootGateError is returned by AssertBootReady when the database isn't safe
// to serve traffic — either because the schema is older than v1.0 or because
// the operator hasn't established a scrub baseline yet (locked decision #10).
// The CLI surfaces Migration verbatim so the user knows the exact remediation.
type BootGateError struct {
	Reason    string
	Migration string
}

func (e *BootGateError) Error() string {
	return fmt.Sprintf("%s — %s", e.Reason, e.Migration)
}

// AssertBootReady refuses startup against a database that has not been
// upgraded to the current schema version AND had its scrub baseline
// established. Fresh DBs stamp the sentinel in DDL; migrated DBs need an
// explicit `migrate --rescrub` or `--no-rescrub` from the operator.
//
// Subcommands that mutate the schema (e.g. migrate) MUST bypass this gate;
// every other code path must call it before serving requests.
func AssertBootReady(conn *sql.DB) error {
	v, err := readUserVersion(conn)
	if err != nil {
		return fmt.Errorf("boot gate: %w", err)
	}
	if v < CurrentSchemaVersion {
		return &BootGateError{
			Reason:    fmt.Sprintf("database is at schema v%d but binary requires v%d", v, CurrentSchemaVersion),
			Migration: "run 'droids-mem migrate --rescrub' to rewrite rows with the current scrub patterns, or 'droids-mem migrate --no-rescrub' to acknowledge that existing plaintext rows stay as-is",
		}
	}
	var value string
	err = conn.QueryRow(`SELECT value FROM meta WHERE key = 'scrub_baseline_complete'`).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && value != "1") {
		return &BootGateError{
			Reason:    "scrub baseline not established on this database",
			Migration: "run 'droids-mem migrate --rescrub' to rewrite all rows with the current scrub patterns, or 'droids-mem migrate --no-rescrub' to acknowledge existing rows stay unscrubbed",
		}
	}
	if err != nil {
		return fmt.Errorf("boot gate: read scrub baseline: %w", err)
	}
	return nil
}
