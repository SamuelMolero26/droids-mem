package db

import (
	"database/sql"
	"fmt"
)

// CurrentSchemaVersion is the user_version that a fully-initialized
// database reports. Bump when adding a new entry to the migrations ladder.
const CurrentSchemaVersion = 3

// migration is one rung in the PRAGMA user_version ladder. Each rung runs
// inside its own transaction; partial failure rolls back atomically.
//
// Migrations MUST be appended in monotonically increasing (from, to) order.
// Rungs are not allowed to skip versions.
type migration struct {
	from int
	to   int
	sql  string
}

// migrations is the ordered ladder applied by Migrate. v0 = pre-v1.0 schema
// (no scope, scrub_pattern_version, scrub_counts, or meta). v1 = v1.0 schema
// matching schema.go ddl for a fresh DB.
//
// v0→v1 only widens the row shape and adds the meta table. The FTS5 tokenizer
// flip (decision #17 in v1.0 plan) lives in `migrate --rescrub` so it can run
// in the same transaction as the row rewrite; the auto-applied ladder leaves
// FTS untouched and lets the boot gate block startup until the operator opts
// in via --rescrub or --no-rescrub.
var migrations = []migration{
	{from: 0, to: 1, sql: migrationV0ToV1},
	{from: 1, to: 2, sql: migrationV1ToV2},
	{from: 2, to: 3, sql: migrationV2ToV3},
}

const migrationV0ToV1 = `
ALTER TABLE memories ADD COLUMN scope TEXT NOT NULL DEFAULT 'shared'
    CHECK(scope IN ('personal','shared'));
ALTER TABLE memories ADD COLUMN scrub_pattern_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE memories ADD COLUMN scrub_counts TEXT;

CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
`

// migrationV1ToV2 adds the Expand signal columns (ADR-0013) and scopes the
// memories_au FTS trigger to the indexed text columns. Purely additive: no row
// rewrite, no re-fingerprint, no scrub baseline interaction — ALTER ADD COLUMN
// is O(1) metadata and the trigger swap is DDL-only with identical behaviour for
// text updates. Auto-applied at boot via the ladder; no `migrate` required.
//
// The trigger DROP/CREATE leaves the FTS *table* untouched, so a v0→v1→v2 DB
// that has not yet run `migrate --rescrub` keeps its trigram FTS; only the
// firing condition of the trigger changes.
const migrationV1ToV2 = `
ALTER TABLE memories ADD COLUMN expand_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memories ADD COLUMN last_expanded_at INTEGER;

DROP TRIGGER IF EXISTS memories_au;
CREATE TRIGGER memories_au
AFTER UPDATE OF title, what, learned, tags ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, title, what, learned, tags)
    VALUES ('delete', OLD.rowid, OLD.title, OLD.what, OLD.learned, OLD.tags);
    INSERT INTO memories_fts(rowid, title, what, learned, tags)
    VALUES (NEW.rowid, NEW.title, NEW.what, NEW.learned, NEW.tags);
END;
`

// migrationV2ToV3 adds the Origin column (ADR-0016): memory provenance,
// 'manual' (default) or 'auto' for machine-composed session-end summaries.
// Purely additive — ALTER ADD COLUMN with a constant default is O(1) metadata,
// and existing rows backfill to 'manual' via the DEFAULT (every pre-v3 memory
// was authored by an explicit decision). The composite index serves the
// auto-summary recency read (recent-sessions) and origin-keyed eviction scan;
// it never touches FTS, the boot gate, or the scrub baseline.
const migrationV2ToV3 = `
ALTER TABLE memories ADD COLUMN origin TEXT NOT NULL DEFAULT 'manual'
    CHECK(origin IN ('manual','auto'));
CREATE INDEX IF NOT EXISTS idx_memories_origin_created ON memories(origin, created_at DESC);
`

// Migrate advances db's schema from its current user_version up to
// CurrentSchemaVersion by running each pending rung in order. Already-current
// databases return nil without touching anything. Each rung is wrapped in
// a transaction with the user_version bump, so partial failures leave the
// database on the prior version.
//
// Migrate does NOT initialize a brand-new database — callers should invoke
// the fresh-DDL path before reaching this (see Init).
func Migrate(db *sql.DB) error {
	v, err := readUserVersion(db)
	if err != nil {
		return err
	}
	for _, m := range migrations {
		if v >= m.to {
			continue
		}
		if v != m.from {
			return fmt.Errorf("schema version gap: db at %d, next migration is %d→%d", v, m.from, m.to)
		}
		if err := applyMigration(db, m); err != nil {
			return fmt.Errorf("migration %d→%d: %w", m.from, m.to, err)
		}
		v = m.to
	}
	return nil
}

// applyMigration runs one ladder rung atomically: BEGIN, exec rung SQL,
// bump user_version, COMMIT. PRAGMA user_version participates in the
// surrounding transaction (it writes to the database header within the
// txn boundary), so a rollback restores the prior version.
func applyMigration(db *sql.DB, m migration) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(m.sql); err != nil {
		return fmt.Errorf("exec migration sql: %w", err)
	}
	// PRAGMA user_version cannot be parameterized; m.to is an int constant
	// we control, so direct interpolation is safe.
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", m.to)); err != nil {
		return fmt.Errorf("bump user_version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func readUserVersion(db *sql.DB) (int, error) {
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("read user_version: %w", err)
	}
	return v, nil
}

func setUserVersion(db *sql.DB, v int) error {
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", v)); err != nil {
		return fmt.Errorf("set user_version: %w", err)
	}
	return nil
}
