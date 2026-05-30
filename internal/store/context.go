package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Tier sizes for the browse tier. Always-tier returns latest session_summary
// + ALL user_rules unconditionally — those are critical state, not a list to
// cap. Browse tier returns titles + short snippets for shallow scanning.
const (
	browseErrorLimit   = 10
	browseTaskLimit    = 10
	browseSnippetChars = 120
)

type ContextRequest struct {
	TaskType string `json:"task_type"`
	Query    string `json:"query"` // optional — falls back to task_type tokens
}

// ContextMemory represents either an always-tier memory (full Learned body)
// or a browse-tier memory (Snippet truncated from What). Tier field tells
// the agent which it is.
type ContextMemory struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Tier      string `json:"tier"`              // "always" | "browse"
	Learned   string `json:"learned,omitempty"` // populated for always tier
	Snippet   string `json:"snippet,omitempty"` // populated for browse tier
	CreatedAt int64  `json:"created_at"`
}

type ContextResponse struct {
	TaskType    string          `json:"task_type"`
	LastSession *ContextMemory  `json:"last_session,omitempty"`
	UserRules   []ContextMemory `json:"user_rules"`
	Browse      []ContextMemory `json:"browse"`
}

func (s *Store) Context(req ContextRequest) (*ContextResponse, error) {
	taskType := strings.ToLower(strings.TrimSpace(req.TaskType))
	if taskType == "" {
		return nil, &ValidationError{Field: "task_type", Message: "required"}
	}

	rawQuery := strings.TrimSpace(req.Query)
	if rawQuery == "" {
		rawQuery = taskType
	}
	ftsQuery := phraseFTSQuery(rawQuery)
	if ftsQuery == "" {
		// rawQuery was all punctuation/no tokens; fall back to the task_type
		// (always a non-empty alphanumeric slug) so the browse tier still ranks.
		ftsQuery = phraseFTSQuery(taskType)
	}

	resp := &ContextResponse{
		TaskType:  taskType,
		UserRules: []ContextMemory{},
		Browse:    []ContextMemory{},
	}

	// All four reads run inside a single BEGIN DEFERRED on a dedicated
	// connection so the returned bundle is a consistent snapshot. Without
	// this, a concurrent writer could commit between fetchLastSession and
	// fetchBrowseTier, producing a bundle that mixes pre- and post-write
	// state (e.g. retention prune deleted the old session_summary, new one
	// not yet committed → LastSession is stale or missing while browse-tier
	// rows reference the new session_id).
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire conn: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN DEFERRED"); err != nil {
		return nil, fmt.Errorf("begin deferred: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			conn.ExecContext(ctx, "ROLLBACK")
		}
	}()

	last, err := fetchLastSessionConn(ctx, conn, taskType)
	if err != nil {
		return nil, err
	}
	if last != nil {
		resp.LastSession = last
	}

	rules, err := fetchAllUserRulesConn(ctx, conn, taskType)
	if err != nil {
		return nil, err
	}
	resp.UserRules = rules

	browse, err := fetchBrowseTierConn(ctx, conn, ftsQuery, taskType)
	if err != nil {
		return nil, err
	}
	resp.Browse = browse

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, fmt.Errorf("commit context: %w", err)
	}
	committed = true

	return resp, nil
}

func fetchLastSessionConn(ctx context.Context, conn *sql.Conn, taskType string) (*ContextMemory, error) {
	var m ContextMemory
	err := conn.QueryRowContext(ctx, `
		SELECT id, kind, title, learned, created_at
		FROM memories
		WHERE task_type = ? AND kind = 'session_summary'
		ORDER BY created_at DESC
		LIMIT 1
	`, taskType).Scan(&m.ID, &m.Kind, &m.Title, &m.Learned, &m.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("fetch last session: %w", err)
	}
	m.Tier = "always"
	return &m, nil
}

func fetchAllUserRulesConn(ctx context.Context, conn *sql.Conn, taskType string) ([]ContextMemory, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT id, kind, title, learned, created_at
		FROM memories
		WHERE task_type = ? AND kind = 'user_rule'
		ORDER BY created_at DESC
	`, taskType)
	if err != nil {
		return nil, fmt.Errorf("fetch user rules: %w", err)
	}
	defer rows.Close()
	out := []ContextMemory{}
	for rows.Next() {
		var m ContextMemory
		if err := rows.Scan(&m.ID, &m.Kind, &m.Title, &m.Learned, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan user rule: %w", err)
		}
		m.Tier = "always"
		out = append(out, m)
	}
	return out, rows.Err()
}

// fetchBrowseTierConn returns top-N error_resolution and task_pattern memories
// ranked by BM25, projected to {id, kind, title, snippet}. Snippet is the
// first browseSnippetChars (runes) of `what`.
func fetchBrowseTierConn(ctx context.Context, conn *sql.Conn, ftsQuery, taskType string) ([]ContextMemory, error) {
	errs, err := fetchBrowseKindConn(ctx, conn, ftsQuery, taskType, "error_resolution", browseErrorLimit)
	if err != nil {
		return nil, err
	}
	patterns, err := fetchBrowseKindConn(ctx, conn, ftsQuery, taskType, "task_pattern", browseTaskLimit)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(errs)+len(patterns))
	out := make([]ContextMemory, 0, len(errs)+len(patterns))
	for _, m := range errs {
		if !seen[m.ID] {
			seen[m.ID] = true
			out = append(out, m)
		}
	}
	for _, m := range patterns {
		if !seen[m.ID] {
			seen[m.ID] = true
			out = append(out, m)
		}
	}
	return out, nil
}

func fetchBrowseKindConn(ctx context.Context, conn *sql.Conn, ftsQuery, taskType, kind string, limit int) ([]ContextMemory, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT m.id, m.kind, m.title, m.what, m.created_at
		FROM memories_fts fts
		JOIN memories m ON m.rowid = fts.rowid
		WHERE memories_fts MATCH ?
		AND m.task_type = ?
		AND m.kind = ?
		ORDER BY bm25(memories_fts, 3, 1, 2, 1)
		LIMIT ?
	`, ftsQuery, taskType, kind, limit)
	if err != nil {
		return nil, fmt.Errorf("fetch browse (%s): %w", kind, err)
	}
	defer rows.Close()
	out := []ContextMemory{}
	for rows.Next() {
		var m ContextMemory
		var what string
		if err := rows.Scan(&m.ID, &m.Kind, &m.Title, &what, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan browse (%s): %w", kind, err)
		}
		m.Tier = "browse"
		m.Snippet = snippet(what, browseSnippetChars)
		out = append(out, m)
	}
	return out, rows.Err()
}

// snippet truncates s to at most n runes (NOT bytes) so multi-byte UTF-8
// (CJK, emoji, accented Latin) is never cut mid-rune. CONTEXT.md defines
// the budget as "120-char", interpreted as runes (Unicode code points).
func snippet(s string, n int) string {
	s = strings.TrimSpace(reWhitespace.ReplaceAllString(s, " "))
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	cut := string(runes[:n])
	// trim to last space within budget so we don't cut mid-word
	if idx := strings.LastIndexByte(cut, ' '); idx > n/2 {
		cut = cut[:idx]
	}
	return cut + "…"
}
