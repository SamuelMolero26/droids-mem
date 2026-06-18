package store

import (
	"context"
	"database/sql"
	"fmt"
)

// expandStatsTopN bounds each leaderboard in the Expand signal report. A
// hardcoded constant — the report is an operator aid, not a paginated surface.
const expandStatsTopN = 10

// ExpandStat is one row in an Expand signal leaderboard.
type ExpandStat struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	TaskType       string `json:"task_type"`
	Title          string `json:"title"`
	ExpandCount    int    `json:"expand_count"`
	LastExpandedAt int64  `json:"last_expanded_at"` // unix seconds; 0 when never expanded
}

// ExpandStatsReport is the shape emitted by `droids-mem doctor --expand-stats`
// (ADR-0013). It surfaces which memories agents expand most, to inform future
// browse-tier sizing. Counts are total-lifetime; the signal excludes operator
// TUI reads (those go through GetRow, which does not record).
type ExpandStatsReport struct {
	Status          string       `json:"status"`
	RowsTotal       int          `json:"rows_total"`
	RowsExpanded    int          `json:"rows_expanded"`
	TotalExpansions int          `json:"total_expansions"`
	TopByCount      []ExpandStat `json:"top_by_count"`
	TopByRecency    []ExpandStat `json:"top_by_recency"`
}

// ExpandStats aggregates the Expand signal columns. No index on expand_count /
// last_expanded_at by design (ADR-0013: an index would tax every Get write);
// doctor is operator-invoked and rare, so the full scan + sort is acceptable.
func (s *Store) ExpandStats() (*ExpandStatsReport, error) {
	ctx := context.Background()
	rep := &ExpandStatsReport{
		Status:       "ok",
		TopByCount:   []ExpandStat{},
		TopByRecency: []ExpandStat{},
	}

	var totalExpansions sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COUNT(CASE WHEN expand_count > 0 THEN 1 END),
		       COALESCE(SUM(expand_count), 0)
		FROM memories
	`).Scan(&rep.RowsTotal, &rep.RowsExpanded, &totalExpansions); err != nil {
		return nil, fmt.Errorf("aggregate expand signal: %w", err)
	}
	rep.TotalExpansions = int(totalExpansions.Int64)

	var err error
	if rep.TopByCount, err = s.expandLeaderboard(ctx,
		`WHERE expand_count > 0 ORDER BY expand_count DESC, id`); err != nil {
		return nil, fmt.Errorf("top by count: %w", err)
	}
	if rep.TopByRecency, err = s.expandLeaderboard(ctx,
		`WHERE last_expanded_at IS NOT NULL ORDER BY last_expanded_at DESC, id`); err != nil {
		return nil, fmt.Errorf("top by recency: %w", err)
	}
	return rep, nil
}

// expandLeaderboard runs the shared projection with a caller-supplied WHERE +
// ORDER BY tail, capped at expandStatsTopN. The tail is a hardcoded string
// (never user input), so the interpolation is safe.
func (s *Store) expandLeaderboard(ctx context.Context, tail string) ([]ExpandStat, error) {
	// #nosec G201 -- tail is one of two compile-time-constant clauses above.
	stmt := fmt.Sprintf(`
		SELECT id, kind, task_type, title, expand_count, COALESCE(last_expanded_at, 0)
		FROM memories %s LIMIT %d
	`, tail, expandStatsTopN)
	rows, err := s.db.QueryContext(ctx, stmt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ExpandStat{}
	for rows.Next() {
		var e ExpandStat
		if err := rows.Scan(&e.ID, &e.Kind, &e.TaskType, &e.Title, &e.ExpandCount, &e.LastExpandedAt); err != nil {
			return nil, fmt.Errorf("scan expand stat: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
