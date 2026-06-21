package store

import (
	"context"
	"database/sql"
	"fmt"
)

// ScrubStatsReport is the shape emitted by `droids-mem doctor --scrub-stats`.
// Aggregate + per_pattern over the durable scrub_counts column. Per-row
// scrub_counts is collapsed via json_extract so the column stays sparse.
type ScrubStatsReport struct {
	Status             string         `json:"status"`
	RowsTotal          int            `json:"rows_total"`
	RowsWithRedactions int            `json:"rows_with_redactions"`
	TotalRedactions    int            `json:"total_redactions"`
	RedactionRate      float64        `json:"redaction_rate"`
	PerPattern         map[string]int `json:"per_pattern"`
	PatternVersion     int            `json:"pattern_version"`
}

// ScrubStats aggregates per-row scrub_counts across memories and returns a
// structured snapshot. Two queries: one for the totals + count of rows with
// non-null scrub_counts, one for per-pattern sums via json_each.
func (s *Store) ScrubStats() (*ScrubStatsReport, error) {
	ctx := context.Background()
	rep := &ScrubStatsReport{
		Status:         "ok",
		PerPattern:     map[string]int{},
		PatternVersion: ScrubPatternVersion,
	}

	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memories`).
		Scan(&rep.RowsTotal); err != nil {
		return nil, fmt.Errorf("count memories: %w", err)
	}

	// rows_with_redactions + total_redactions in one pass. COALESCE on SUM
	// covers the no-rows-with-redactions case so the row scan never sees
	// NULL where we expect an int.
	var totalRedactions sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(json_extract(scrub_counts, '$.redaction_count')), 0)
		FROM memories
		WHERE scrub_counts IS NOT NULL
	`).Scan(&rep.RowsWithRedactions, &totalRedactions); err != nil {
		return nil, fmt.Errorf("aggregate scrub_counts: %w", err)
	}
	rep.TotalRedactions = int(totalRedactions.Int64)
	if rep.RowsTotal > 0 {
		rep.RedactionRate = float64(rep.RowsWithRedactions) / float64(rep.RowsTotal)
	}

	// Per-pattern via json_each on the per_pattern_counts map. Empty result
	// when no row has fired any pattern — left as the zero-value empty map.
	rows, err := s.db.QueryContext(ctx, `
		SELECT je.key, COALESCE(SUM(je.value), 0)
		FROM memories,
		     json_each(json_extract(memories.scrub_counts, '$.per_pattern_counts')) je
		WHERE memories.scrub_counts IS NOT NULL
		GROUP BY je.key
	`)
	if err != nil {
		return nil, fmt.Errorf("per-pattern aggregate: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			return nil, fmt.Errorf("scan per-pattern row: %w", err)
		}
		rep.PerPattern[name] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return rep, nil
}
