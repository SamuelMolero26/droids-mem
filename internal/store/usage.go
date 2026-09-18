package store

import (
	"context"
	"fmt"
	"sort"
)

// KindSize is one kind's share of a project's payload: row count plus the
// summed payload bytes of title, what, learned, and tags.
type KindSize struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
	Bytes int64  `json:"bytes"`
}

// ProjectSize is one task_type's live payload footprint: row count, summed
// payload bytes, and the per-kind split ordered by bytes descending.
type ProjectSize struct {
	TaskType string     `json:"task_type"`
	Count    int        `json:"count"`
	Bytes    int64      `json:"bytes"`
	ByKind   []KindSize `json:"by_kind"`
}

// ProjectSizes returns every task_type's live payload footprint, ordered by
// bytes descending with a task_type-ascending tiebreak. Row bytes are
// COALESCE(octet_length(title),0)+COALESCE(octet_length(what),0)+
// COALESCE(octet_length(learned),0)+COALESCE(octet_length(tags),0) — UTF-8
// bytes, not runes — summed per row. The db-file size is deliberately out of
// scope here (the TUI stats it best-effort); so are cached counters,
// triggers, and migrations — this is one grouped scan, assembled in Go.
func (s *Store) ProjectSizes(ctx context.Context) ([]ProjectSize, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT task_type, kind, COUNT(*),
		       SUM(COALESCE(octet_length(title),0) + COALESCE(octet_length(what),0)
		         + COALESCE(octet_length(learned),0) + COALESCE(octet_length(tags),0))
		FROM memories GROUP BY task_type, kind
	`)
	if err != nil {
		return nil, fmt.Errorf("project sizes query: %w", err)
	}
	defer rows.Close()

	byProject := map[string]*ProjectSize{}
	for rows.Next() {
		var taskType, kind string
		var count int
		var bytes int64
		if err := rows.Scan(&taskType, &kind, &count, &bytes); err != nil {
			return nil, fmt.Errorf("scan project size: %w", err)
		}
		p, ok := byProject[taskType]
		if !ok {
			p = &ProjectSize{TaskType: taskType}
			byProject[taskType] = p
		}
		p.Count += count
		p.Bytes += bytes
		p.ByKind = append(p.ByKind, KindSize{Kind: kind, Count: count, Bytes: bytes})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("project sizes rows: %w", err)
	}

	out := make([]ProjectSize, 0, len(byProject))
	for _, p := range byProject {
		sort.Slice(p.ByKind, func(i, j int) bool { return p.ByKind[i].Bytes > p.ByKind[j].Bytes })
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Bytes != out[j].Bytes {
			return out[i].Bytes > out[j].Bytes
		}
		return out[i].TaskType < out[j].TaskType
	})
	return out, nil
}
