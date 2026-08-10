package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/samuelmolero26/droids-mem/internal/db"
	"github.com/samuelmolero26/droids-mem/internal/scrub"
)

// MigrateOptions controls the scrub-baseline migration. Exactly one mode is
// expected at this layer — the CLI is responsible for rejecting ambiguous
// flag combos before calling.
type MigrateOptions struct {
	// Rescrub rewrites every memory row with the current scrub patterns,
	// refreshing its fingerprint + scrub_counts. The tokenizer flip (porter
	// stemmer) is owned by the db ladder's rung 7→8, applied automatically at
	// boot — Migrate never touches the index. When false, Migrate runs the
	// lighter `--no-rescrub` path: it stamps the scrub-baseline sentinel
	// without touching row bodies.
	Rescrub bool
}

// MigrateSummary is the structured result the CLI emits. Counts cover what
// the operator actually paid for so output sizes can be sanity-checked.
type MigrateSummary struct {
	Mode               string `json:"mode"`                        // "rescrub" | "no-rescrub"
	RowsScanned        int    `json:"rows_scanned"`                // total rows visited
	RowsRewritten      int    `json:"rows_rewritten"`              // rows whose title/what/learned actually changed after scrub
	RowsWithRedactions int    `json:"rows_with_redactions"`        // subset whose scrub fired
	TotalRedactions    int    `json:"total_redactions"`            // sum across all fields
	BaselineSet        bool   `json:"scrub_baseline_complete_set"` // sentinel persisted
	PatternVersion     int    `json:"pattern_version"`             // scrub.Version used
}

// SchemaVersionError aborts store.Migrate when the database's user_version is
// not the binary's CurrentSchemaVersion. The ladder owns schema shape
// (including the rung 7→8 tokenizer flip); store.Migrate only performs the
// optional rescrub rewrite + sentinel stamp and must not run against a schema
// the ladder has not brought current yet.
//
// This is defensive/test-only on the normal path — the CLI always opens via
// db.Open → Init, which ladders first. The message deliberately says to open
// with a current binary, never "run migrate --rescrub", so an old binary on a
// NEWER database is not mis-advised (its ladder cannot catch up).
type SchemaVersionError struct {
	Current  int // the database's user_version
	Required int // db.CurrentSchemaVersion the ladder targets
}

func (e *SchemaVersionError) Error() string {
	return fmt.Sprintf("cannot migrate: database schema is v%d but the migration ladder requires v%d — open the database with a current binary once to apply pending ladder rungs", e.Current, e.Required)
}

// FingerprintCollisionError aborts --rescrub when two rows converge to the
// same post-scrub fingerprint. The operator resolves the duplicate; there is
// no auto-merge (locked decision 3).
type FingerprintCollisionError struct {
	RowID        string // row being rewritten
	CollidesWith string // existing row already holding the fingerprint
	Fingerprint  string
}

// Error carries the full remediation, not just the diagnosis, because on the
// auto-migration path this string is all the operator gets: the boot gate
// returns its own error and only logs this one, so the `migrate` command's
// suggestion field never renders. Deduping the row requires `prune`, which
// does not bypass the gate — hence the --no-rescrub step.
func (e *FingerprintCollisionError) Error() string {
	return fmt.Sprintf("rescrub collision: row %q re-fingerprints to %q, already held by row %q — delete or merge one of the two, then re-run 'droids-mem migrate --rescrub'. While the boot gate is still closed no command can reach the rows: open it first with 'droids-mem migrate --no-rescrub', which marks the scrub baseline complete on still-unscrubbed rows — so the closing --rescrub is required, not optional",
		e.RowID, e.Fingerprint, e.CollidesWith)
}

// Migrate establishes the scrub baseline on s.DB(): optionally rewrites every
// row through the current scrub patterns and stamps the
// meta.scrub_baseline_complete='1' sentinel the boot gate checks. It is
// intended to be invoked by the `migrate` subcommand and runs atomically: any
// failure rolls the database back to its pre-migrate shape.
//
// Schema shape (including the tokenizer flip) is the ladder's job — see
// SchemaVersionError. Migrate performs no schema or index work of its own.
func Migrate(s *Store, opts MigrateOptions) (*MigrateSummary, error) {
	summary := &MigrateSummary{
		Mode:           "no-rescrub",
		PatternVersion: scrub.Version,
	}
	if opts.Rescrub {
		summary.Mode = "rescrub"
	}

	ctx := context.Background()
	conn, err := s.DB().Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire conn: %w", err)
	}
	defer conn.Close()

	// Precondition (read-only, before any write): refuse to run the data
	// migration against a schema the ladder has not brought current yet.
	var v int
	if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return nil, fmt.Errorf("read user_version: %w", err)
	}
	if v != db.CurrentSchemaVersion {
		return nil, &SchemaVersionError{Current: v, Required: db.CurrentSchemaVersion}
	}

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, fmt.Errorf("begin immediate: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()

	if opts.Rescrub {
		if err := rewriteAllRows(ctx, conn, summary); err != nil {
			return nil, fmt.Errorf("rescrub rows: %w", err)
		}
	}

	if _, err := conn.ExecContext(ctx, `
		INSERT INTO meta(key, value) VALUES('scrub_baseline_complete', '1')
		ON CONFLICT(key) DO UPDATE SET value = '1'
	`); err != nil {
		return nil, fmt.Errorf("set scrub baseline sentinel: %w", err)
	}
	summary.BaselineSet = true

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, fmt.Errorf("commit migrate: %w", err)
	}
	committed = true
	return summary, nil
}

// rewriteAllRows walks every memory row, re-runs the current scrub patterns,
// refreshes the row's fingerprint (decision #18 — the fingerprint normalizer
// changed in v1.0 too), and persists the updated scrub_counts + bump in
// scrub_pattern_version. Streams via two passes (collect then apply) so the
// rows.Rows iterator and the UPDATEs don't share the connection mid-iteration.
func rewriteAllRows(ctx context.Context, conn *sql.Conn, summary *MigrateSummary) error {
	type rowState struct {
		id       string
		taskType string
		kind     string
		title    string
		what     string
		learned  string
		report   scrub.ScrubReport
		fp       string
		changed  bool
	}

	// Collect pass runs in a closure so `defer rows.Close()` releases the
	// iterator before the apply pass reuses the same connection for UPDATEs.
	states, err := func() ([]rowState, error) {
		rows, err := conn.QueryContext(ctx, `
			SELECT id, task_type, kind, title, what, learned FROM memories
		`)
		if err != nil {
			return nil, fmt.Errorf("scan memories: %w", err)
		}
		defer rows.Close()
		var states []rowState
		for rows.Next() {
			var st rowState
			if err := rows.Scan(&st.id, &st.taskType, &st.kind, &st.title, &st.what, &st.learned); err != nil {
				return nil, fmt.Errorf("scan row: %w", err)
			}
			states = append(states, st)
		}
		return states, rows.Err()
	}()
	if err != nil {
		return err
	}

	summary.RowsScanned = len(states)

	// fpOwners maps each post-scrub fingerprint to the first row holding it,
	// so a re-fingerprint collision is detected in the collect pass — BEFORE
	// the apply pass issues any UPDATE. The txn rollback remains the backstop
	// for anything this misses (e.g. a DB-level UNIQUE violation).
	fpOwners := make(map[string]string, len(states))
	for i := range states {
		st := &states[i]
		titleOut, titleRep := scrub.Scrub(st.title)
		whatOut, whatRep := scrub.Scrub(st.what)
		learnedOut, learnedRep := scrub.Scrub(st.learned)

		st.report = aggregateScrubReports(titleRep, whatRep, learnedRep)
		st.changed = titleOut != st.title || whatOut != st.what || learnedOut != st.learned
		st.title = titleOut
		st.what = whatOut
		st.learned = learnedOut
		st.fp = fingerprint(st.taskType, st.kind, st.title, st.learned)

		if owner, exists := fpOwners[st.fp]; exists {
			return &FingerprintCollisionError{RowID: st.id, CollidesWith: owner, Fingerprint: st.fp}
		}
		fpOwners[st.fp] = st.id

		if st.report.RedactionCount > 0 {
			summary.RowsWithRedactions++
			summary.TotalRedactions += st.report.RedactionCount
		}
	}

	updateStmt, err := conn.PrepareContext(ctx, `
		UPDATE memories
		SET title = ?, what = ?, learned = ?, fingerprint = ?,
		    scrub_pattern_version = ?, scrub_counts = ?
		WHERE id = ?
	`)
	if err != nil {
		return fmt.Errorf("prepare update: %w", err)
	}
	defer updateStmt.Close()

	for _, st := range states {
		var counts sql.NullString
		if st.report.RedactionCount > 0 {
			b, err := json.Marshal(st.report)
			if err != nil {
				return fmt.Errorf("marshal scrub_counts for %s: %w", st.id, err)
			}
			counts = sql.NullString{String: string(b), Valid: true}
		}
		if _, err := updateStmt.ExecContext(ctx,
			st.title, st.what, st.learned, st.fp,
			scrub.Version, counts, st.id,
		); err != nil {
			return fmt.Errorf("update %s: %w", st.id, err)
		}
		// rows_rewritten counts rows whose content actually changed after
		// scrub, not UPDATEs issued (SM-R4).
		if st.changed {
			summary.RowsRewritten++
		}
	}
	return nil
}
