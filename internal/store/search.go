package store

import (
	"fmt"
	"strings"
)

const (
	defaultSearchLimit = 5
	maxSearchLimit     = 20
)

type SearchRequest struct {
	Query    string `json:"query"`
	TaskType string `json:"task_type"` // optional filter
	Kind     string `json:"kind"`      // optional filter
	Limit    int    `json:"limit"`     // default 5, max 20
}

type SearchResult struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind"`
	Title     string  `json:"title"`
	Learned   string  `json:"learned"`
	TaskType  string  `json:"task_type"`
	CreatedAt int64   `json:"created_at"`
	Score     float64 `json:"score"` // BM25 rank — more negative = better match
}

type SearchResponse struct {
	Results []SearchResult `json:"results"`
	Total   int            `json:"total"`
}

func (s *Store) Search(req SearchRequest) (*SearchResponse, error) {
	if strings.TrimSpace(req.Query) == "" {
		return nil, &ValidationError{Field: "query", Message: "required"}
	}
	if req.Kind != "" && !validKinds[req.Kind] {
		return nil, &ValidationError{Field: "kind", Message: "must be one of: error_resolution, task_pattern, user_rule, session_summary"}
	}

	limit := req.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}

	ftsQuery := sanitizeFTSQuery(req.Query)

	// build WHERE clause — only hardcoded strings in the format string, user values in args
	conditions := []string{"memories_fts MATCH ?"}
	args := []any{ftsQuery}

	if req.TaskType != "" {
		conditions = append(conditions, "m.task_type = ?")
		args = append(args, req.TaskType)
	}
	if req.Kind != "" {
		conditions = append(conditions, "m.kind = ?")
		args = append(args, req.Kind)
	}
	whereClause := strings.Join(conditions, " AND ")

	// count total matches before pagination so callers can decide whether
	// to fetch more pages. Same WHERE, no LIMIT.
	countStmt := fmt.Sprintf(`
		SELECT count(*)
		FROM memories_fts fts
		JOIN memories m ON m.rowid = fts.rowid
		WHERE %s
	`, whereClause)
	var total int
	if err := s.db.QueryRow(countStmt, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("search count: %w", err)
	}

	pageArgs := append(args, limit)
	stmt := fmt.Sprintf(`
		SELECT m.id, m.kind, m.title, m.learned, m.task_type, m.created_at, fts.rank
		FROM memories_fts fts
		JOIN memories m ON m.rowid = fts.rowid
		WHERE %s
		ORDER BY fts.rank
		LIMIT ?
	`, whereClause)

	rows, err := s.db.Query(stmt, pageArgs...)
	if err != nil {
		return nil, fmt.Errorf("search query: %w", err)
	}
	defer rows.Close()

	results := []SearchResult{}
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.ID, &r.Kind, &r.Title, &r.Learned, &r.TaskType, &r.CreatedAt, &r.Score); err != nil {
			return nil, fmt.Errorf("scan result: %w", err)
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search rows: %w", err)
	}

	return &SearchResponse{Results: results, Total: total}, nil
}

// sanitizeFTSQuery strips FTS5 operator chars to prevent MATCH injection.
// User values always go through ? placeholders; only the query string itself
// needs sanitization.
//
// `*` is also stripped — trigram tokenizer (see schema.go) provides substring
// match natively, so prefix wildcards are unnecessary and would cause the
// FTS5 parser to error on bare `*`.
//
// Asymmetry with save.go nearDuplicateConn (which quotes every token as a
// phrase literal): this path is caller-driven — the query string carries
// caller intent and operator chars like `-` (NOT) are preserved on purpose.
// nearDuplicateConn treats Memory content as the query (no operator intent),
// so it locks down harder. See ADR-0003.
func sanitizeFTSQuery(q string) string {
	q = strings.NewReplacer(`"`, ``, `(`, ``, `)`, ``, `*`, ``, `^`, ``).Replace(q)
	return strings.TrimSpace(reWhitespace.ReplaceAllString(q, " "))
}
