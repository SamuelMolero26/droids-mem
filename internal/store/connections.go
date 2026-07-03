package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// defaultNeighborLimit caps how many connected memories the inspector's
// CONNECTIONS widget shows (ADR-0021 detail-pane fast-follow).
const defaultNeighborLimit = 5

// Neighbor is one connected memory: a related row surfaced by the same
// BM25-top-K → Jaccard machinery the save path uses for near-duplicate
// detection, but cross-kind and cross-task (connections span the corpus) and
// with no dedupe threshold — it ranks whatever is most similar.
type Neighbor struct {
	ID       string  `json:"id"`
	Kind     string  `json:"kind"`
	Title    string  `json:"title"`
	TaskType string  `json:"task_type"`
	Score    float64 `json:"score"` // Jaccard token-set similarity to the seed
}

// Neighbors returns the memories most similar to the one identified by id,
// ranked by Jaccard token-set overlap. It reuses the near-duplicate retrieval
// shape (dedupeTokens → capped OR-of-phrases BM25 candidates → Jaccard rerank)
// but without the task_type/kind filter and without the dedupe gate — a
// connection is just "most related", not "duplicate". Read-only and
// non-counting; the seed is excluded and zero-overlap candidates are dropped.
func (s *Store) Neighbors(ctx context.Context, id string, limit int) ([]Neighbor, error) {
	if strings.TrimSpace(id) == "" {
		return nil, &ValidationError{Field: "id", Message: "required"}
	}
	if limit <= 0 {
		limit = defaultNeighborLimit
	}

	// Load the seed's searchable text in one shot.
	var title, what, learned, tags string
	err := s.db.QueryRowContext(ctx,
		`SELECT title, what, learned, tags FROM memories WHERE id = ?`, id,
	).Scan(&title, &what, &learned, &tags)
	if errors.Is(err, sql.ErrNoRows) {
		return []Neighbor{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("neighbors seed: %w", err)
	}

	terms, seedTokens := dedupeTokens(title + " " + what + " " + learned + " " + tags)
	if len(terms) == 0 {
		return []Neighbor{}, nil
	}
	if len(terms) > bm25QueryTermCap {
		sortTermsByIDF(terms)
		terms = terms[:bm25QueryTermCap]
	}
	quoted := make([]string, len(terms))
	for i, t := range terms {
		quoted[i] = `"` + t + `"`
	}
	query := strings.Join(quoted, " OR ")

	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.kind, m.title, m.task_type, m.what, m.learned, m.tags
		FROM memories_fts fts
		JOIN memories m ON m.rowid = fts.rowid
		WHERE memories_fts MATCH ?
		AND m.id != ?
		ORDER BY bm25(memories_fts, 3, 1, 2, 1)
		LIMIT ?
	`, query, id, bm25CandidateLimit)
	if err != nil {
		return nil, fmt.Errorf("neighbors candidates: %w", err)
	}
	defer rows.Close()

	var out []Neighbor
	for rows.Next() {
		var n Neighbor
		var cWhat, cLearned, cTags string
		if err := rows.Scan(&n.ID, &n.Kind, &n.Title, &n.TaskType, &cWhat, &cLearned, &cTags); err != nil {
			return nil, fmt.Errorf("scan neighbor: %w", err)
		}
		n.Score = jaccard(seedTokens, tokenSet(n.Title+" "+cWhat+" "+cLearned+" "+cTags))
		if n.Score > 0 {
			out = append(out, n)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("neighbors rows: %w", err)
	}

	sort.Slice(out, func(a, b int) bool { return out[a].Score > out[b].Score })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
