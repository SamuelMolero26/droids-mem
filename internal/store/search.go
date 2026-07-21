package store

import (
	"context"
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
	ID             string  `json:"id"`
	Kind           string  `json:"kind"`
	Title          string  `json:"title"`
	Learned        string  `json:"learned"`
	TaskType       string  `json:"task_type"`
	CreatedAt      int64   `json:"created_at"`
	Score          float64 `json:"score"` // BM25 rank — more negative = better match
	ExpandCount    int     `json:"expand_count"`
	LastExpandedAt int64   `json:"last_expanded_at,omitempty"`
}

type SearchResponse struct {
	Results []SearchResult `json:"results"`
	Total   int            `json:"total"`
}

func (s *Store) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
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

	ftsQuery := phraseFTSQuery(req.Query)
	if ftsQuery == "" {
		// Query had no searchable tokens (e.g. all punctuation). Nothing can
		// match; return empty rather than run MATCH on an empty expression.
		return &SearchResponse{Results: []SearchResult{}, Total: 0}, nil
	}

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
	// #nosec G201 -- whereClause is assembled from hardcoded condition
	// strings only; every user value is bound via ? placeholders.
	countStmt := fmt.Sprintf(`
		SELECT count(*)
		FROM memories_fts fts
		JOIN memories m ON m.rowid = fts.rowid
		WHERE %s
	`, whereClause)
	var total int
	if err := s.db.QueryRowContext(ctx, countStmt, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("search count: %w", err)
	}

	pageArgs := append(args, limit)
	// #nosec G201 -- same as above: hardcoded conditions, parameterized values.
	stmt := fmt.Sprintf(`
		SELECT m.id, m.kind, m.title, m.learned, m.task_type, m.created_at, fts.rank,
		       m.expand_count, COALESCE(m.last_expanded_at, 0)
		FROM memories_fts fts
		JOIN memories m ON m.rowid = fts.rowid
		WHERE %s
		ORDER BY fts.rank
		LIMIT ?
	`, whereClause)

	rows, err := s.db.QueryContext(ctx, stmt, pageArgs...)
	if err != nil {
		return nil, fmt.Errorf("search query: %w", err)
	}
	defer rows.Close()

	results := []SearchResult{}
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.ID, &r.Kind, &r.Title, &r.Learned, &r.TaskType, &r.CreatedAt, &r.Score,
			&r.ExpandCount, &r.LastExpandedAt); err != nil {
			return nil, fmt.Errorf("scan result: %w", err)
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search rows: %w", err)
	}

	return &SearchResponse{Results: results, Total: total}, nil
}

// phraseFTSQuery converts arbitrary user text into a safe FTS5 MATCH expression.
//
// Every whitespace-delimited token is wrapped as a quoted phrase literal and the
// tokens are OR-joined. Quoting is the FTS5-sanctioned "treat as literal"
// construct: inside "..." no character carries operator meaning, so no user input
// — comma, colon, paren, or an OR/NOT/NEAR keyword — can be (mis)parsed as query
// syntax. The only char that can escape a phrase is a literal double-quote, which
// FTS5 expects doubled ("").
//
// This supersedes the earlier strip-operator-chars blocklist, which crashed on any
// unlisted syntax char (e.g. `fts5: syntax error near ","`). Robustness is by
// construction here, not by enumeration. No caller of Search or Context passes
// FTS5 operator DSL — both feed free-text (user prompts, TUI search box) — so
// OR-of-phrases loses no real capability. This also unifies with save.go's
// nearDuplicateConn, which already quotes. See ADR-0003.
//
// Returns "" when the input has no tokens (e.g. all punctuation); callers MUST
// treat that as "no searchable terms" and skip the MATCH rather than run it on "".
func phraseFTSQuery(q string) string {
	parts := strings.Fields(q)
	if len(parts) == 0 {
		return ""
	}
	quoted := make([]string, len(parts))
	for i, p := range parts {
		quoted[i] = `"` + strings.ReplaceAll(p, `"`, `""`) + `"`
	}
	return strings.Join(quoted, " OR ")
}

// TokenOverlap returns the fraction of query tokens (3+ chars, lowercased)
// that appear literally in text. It is the corpus-size-invariant relevance
// signal for the UserPromptSubmit recall gate (ADR-0016 pt 8): BM25 rank
// magnitudes scale with corpus size (FTS5 IDF ≈ 0 on tiny DBs), so an
// absolute rank floor cannot separate a junk single-word OR-match from a
// genuine one across DB sizes — token overlap can.
//
// Literal equality only: porter stems match in FTS ("mapping" finds "map")
// but not here, so the floor must stay lenient; tune with the T1.2 recall
// eval (ADR-0016 open item).
func TokenOverlap(query, text string) float64 {
	q := tokenSet(query)
	if len(q) == 0 {
		return 0
	}
	d := tokenSet(text)
	hits := 0
	for t := range q {
		if _, ok := d[t]; ok {
			hits++
		}
	}
	return float64(hits) / float64(len(q))
}
