package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/samuelmolero26/droids-mem/internal/db"
)

// MigrateOptions controls the v1.0 baseline migration. Exactly one mode is
// expected at this layer — the CLI is responsible for rejecting ambiguous
// flag combos before calling.
type MigrateOptions struct {
	// Rescrub rewrites every memory row with the current scrub patterns,
	// refreshes its fingerprint + scrub_counts, and drops + recreates the FTS
	// index with the v1.0 tokenizer. When false, Migrate runs the lighter
	// `--no-rescrub` path: it still flips the FTS tokenizer (so search
	// behavior is uniform across DBs) but leaves row bodies untouched.
	Rescrub bool
}

// MigrateSummary is the structured result the CLI emits. Counts cover what
// the operator actually paid for so output sizes can be sanity-checked.
type MigrateSummary struct {
	Mode               string `json:"mode"`                        // "rescrub" | "no-rescrub"
	RowsScanned        int    `json:"rows_scanned"`                // total rows visited
	RowsRewritten      int    `json:"rows_rewritten"`              // UPDATEs issued
	RowsWithRedactions int    `json:"rows_with_redactions"`        // subset whose scrub fired
	TotalRedactions    int    `json:"total_redactions"`            // sum across all fields
	FTSRebuilt         bool   `json:"fts_rebuilt"`                 // true once the tokenizer flip lands
	BaselineSet        bool   `json:"scrub_baseline_complete_set"` // sentinel persisted
	PatternVersion     int    `json:"pattern_version"`             // ScrubPatternVersion used
}

// Migrate establishes the v1.0 scrub baseline on s.DB(). It is intended to
// be invoked by the `migrate` subcommand and runs atomically: any failure
// rolls the database back to its pre-migrate shape. The boot gate uses the
// `meta.scrub_baseline_complete='1'` sentinel this function sets, so callers
// can read it back via db.AssertBootReady afterward.
func Migrate(s *Store, opts MigrateOptions) (*MigrateSummary, error) {
	summary := &MigrateSummary{
		PatternVersion: ScrubPatternVersion,
	}
	if opts.Rescrub {
		summary.Mode = "rescrub"
	} else {
		summary.Mode = "no-rescrub"
	}

	ctx := context.Background()
	conn, err := s.DB().Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire conn: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, fmt.Errorf("begin immediate: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
	}()

	// Tear down the FTS index + triggers up front so subsequent row UPDATEs
	// (when --rescrub) don't fire trigger writes against a soon-to-be-dropped
	// table. The recreate at the end of the migration uses the v1.0 DDL.
	if err := dropFTSAndTriggers(ctx, conn); err != nil {
		return nil, fmt.Errorf("drop FTS for rebuild: %w", err)
	}

	if opts.Rescrub {
		if err := rewriteAllRows(ctx, conn, summary); err != nil {
			return nil, fmt.Errorf("rescrub rows: %w", err)
		}
	}

	if _, err := conn.ExecContext(ctx, db.FTSSchema); err != nil {
		return nil, fmt.Errorf("recreate FTS: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO memories_fts(rowid, title, what, learned, tags)
		SELECT rowid, title, what, learned, tags FROM memories
	`); err != nil {
		return nil, fmt.Errorf("reindex FTS: %w", err)
	}
	summary.FTSRebuilt = true

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

func dropFTSAndTriggers(ctx context.Context, conn *sql.Conn) error {
	stmts := []string{
		`DROP TRIGGER IF EXISTS memories_ai`,
		`DROP TRIGGER IF EXISTS memories_ad`,
		`DROP TRIGGER IF EXISTS memories_au`,
		`DROP TABLE IF EXISTS memories_fts`,
	}
	for _, stmt := range stmts {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return nil
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
		report   ScrubReport
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

	for i := range states {
		st := &states[i]
		titleOut, titleRep := Scrub(st.title)
		whatOut, whatRep := Scrub(st.what)
		learnedOut, learnedRep := Scrub(st.learned)

		st.report = aggregateScrubReports(titleRep, whatRep, learnedRep)
		st.changed = titleOut != st.title || whatOut != st.what || learnedOut != st.learned
		st.title = titleOut
		st.what = whatOut
		st.learned = learnedOut
		st.fp = fingerprint(st.taskType, st.kind, st.title, st.learned)

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
			ScrubPatternVersion, counts, st.id,
		); err != nil {
			return fmt.Errorf("update %s: %w", st.id, err)
		}
		summary.RowsRewritten++
	}
	return nil
}
