package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Tier sizes for the browse tier. Always-tier returns latest session_summary
// + the newest maxAlwaysTierUserRules user_rules in full; older rules surface
// as browse-tier title stubs (ADR-0011) so no rule is ever invisible. Browse
// tier returns titles + short snippets for shallow scanning.
const (
	browseErrorLimit   = 10
	browseTaskLimit    = 10
	browseSnippetChars = 120
	// deep mode returns full bodies, ~100× a snippet, so its browse tier is
	// deliberately tighter than orient's to keep the bundle within the context
	// budget it exists to spend wisely (ADR-0012).
	deepErrorLimit = 5
	deepTaskLimit  = 5
)

// ContextMode selects retrieval depth for a Context bundle (ADR-0012).
type ContextMode string

const (
	ModeOrient  ContextMode = "orient"  // default: always tier + browse snippets
	ModeDeep    ContextMode = "deep"    // always tier (all rules full) + browse full bodies
	ModeRefresh ContextMode = "refresh" // always tier only — cheap mid-run re-anchor
)

type ContextRequest struct {
	TaskType string      `json:"task_type"`
	Query    string      `json:"query"`          // optional — falls back to task_type tokens
	Mode     ContextMode `json:"mode,omitempty"` // optional — defaults to orient
}

// ContextMemory represents either an always-tier memory (full Learned body)
// or a browse-tier memory (Snippet truncated from What). Tier field tells
// the agent which it is.
type ContextMemory struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Tier      string `json:"tier"`              // "always" | "browse"
	Learned   string `json:"learned,omitempty"` // populated for always tier (and deep browse)
	What      string `json:"what,omitempty"`    // populated for browse items in deep mode
	Snippet   string `json:"snippet,omitempty"` // populated for browse tier (orient)
	CreatedAt int64  `json:"created_at"`
}

type ContextResponse struct {
	TaskType    string          `json:"task_type"`
	LastSession *ContextMemory  `json:"last_session,omitempty"`
	UserRules   []ContextMemory `json:"user_rules"`
	// UserRulesTotal counts all user_rule rows for the task_type. When it
	// exceeds len(UserRules), the overflow appears in Browse as title stubs.
	UserRulesTotal int             `json:"user_rules_total"`
	Browse         []ContextMemory `json:"browse"`
}

func (s *Store) Context(ctx context.Context, req ContextRequest) (*ContextResponse, error) {
	taskType := strings.ToLower(strings.TrimSpace(req.TaskType))
	if taskType == "" {
		return nil, &ValidationError{Field: "task_type", Message: "required"}
	}

	mode := ContextMode(strings.ToLower(strings.TrimSpace(string(req.Mode))))
	if mode == "" {
		mode = ModeOrient
	}
	switch mode {
	case ModeOrient, ModeDeep, ModeRefresh:
	default:
		return nil, &ValidationError{
			Field:      "mode",
			Message:    "must be one of: orient, deep, refresh",
			Retryable:  true,
			Suggestion: "use --mode orient, deep, or refresh",
		}
	}
	// refresh is always-tier only and never ranks a browse tier, so a query
	// would silently do nothing — reject it rather than accept a no-op flag.
	if mode == ModeRefresh && strings.TrimSpace(req.Query) != "" {
		return nil, &ValidationError{
			Field:      "query",
			Message:    "query has no effect in refresh mode",
			Retryable:  true,
			Suggestion: "drop --query, or switch to --mode orient or deep for ranked browse",
		}
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
			// Background ctx so cleanup still runs after request cancellation.
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	last, err := fetchLastSessionConn(ctx, conn, taskType)
	if err != nil {
		return nil, err
	}
	if last != nil {
		resp.LastSession = last
	}

	// deep expands every overflow user_rule to full body (fullCap < 0 → no
	// stubs); orient/refresh keep the always-tier cap and surface the rest as
	// stubs (which refresh then discards).
	fullCap := maxAlwaysTierUserRules
	if mode == ModeDeep {
		fullCap = -1
	}
	rules, ruleStubs, rulesTotal, err := fetchUserRulesConn(ctx, conn, taskType, fullCap)
	if err != nil {
		return nil, err
	}
	resp.UserRules = rules
	resp.UserRulesTotal = rulesTotal

	switch mode {
	case ModeRefresh:
		// Always tier only — no browse, no rule stubs. Cheap re-anchor.
	case ModeDeep:
		// Full bodies, tighter limits; rules are already all full above, so no
		// stubs lead the browse tier.
		browse, err := fetchBrowseTierConn(ctx, conn, ftsQuery, taskType, true, deepErrorLimit, deepTaskLimit)
		if err != nil {
			return nil, err
		}
		resp.Browse = browse
	default: // ModeOrient
		browse, err := fetchBrowseTierConn(ctx, conn, ftsQuery, taskType, false, browseErrorLimit, browseTaskLimit)
		if err != nil {
			return nil, err
		}
		// Rule stubs lead the browse tier: rules are critical state, so their
		// titles must be seen before the BM25-ranked errors/patterns (ADR-0011).
		resp.Browse = append(ruleStubs, browse...)
	}

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

// maxAlwaysTierUserRules bounds the always-tier user_rule slice so the
// context bundle payload stays within the PRD §3.2 budget. Locked decision
// #20, amended by ADR-0011: older rules are not silently dropped — they come
// back as browse-tier title stubs the agent can expand via mem_get.
const maxAlwaysTierUserRules = 5

// fetchUserRulesConn returns the newest rules in full body (always tier) up to
// fullCap, the remainder as browse-tier title stubs, and the total rule count
// for the task_type. A negative fullCap means "no cap" — every rule comes back
// as a full-body always-tier item and stubs is empty (deep mode, ADR-0012).
func fetchUserRulesConn(ctx context.Context, conn *sql.Conn, taskType string, fullCap int) (rules, stubs []ContextMemory, total int, err error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT id, kind, title, learned, created_at
		FROM memories
		WHERE task_type = ? AND kind = 'user_rule'
		ORDER BY created_at DESC
	`, taskType)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("fetch user rules: %w", err)
	}
	defer rows.Close()
	rules = []ContextMemory{}
	stubs = []ContextMemory{}
	for rows.Next() {
		var m ContextMemory
		var learned string
		if err := rows.Scan(&m.ID, &m.Kind, &m.Title, &learned, &m.CreatedAt); err != nil {
			return nil, nil, 0, fmt.Errorf("scan user rule: %w", err)
		}
		total++
		if fullCap < 0 || len(rules) < fullCap {
			m.Tier = "always"
			m.Learned = learned
			rules = append(rules, m)
		} else {
			m.Tier = "browse"
			stubs = append(stubs, m)
		}
	}
	return rules, stubs, total, rows.Err()
}

// fetchBrowseTierConn returns top-N error_resolution and task_pattern memories
// ranked by BM25. When full is false (orient) each item carries a snippet of
// `what`; when true (deep) each carries the full `what`+`learned` body instead.
func fetchBrowseTierConn(ctx context.Context, conn *sql.Conn, ftsQuery, taskType string, full bool, errLimit, taskLimit int) ([]ContextMemory, error) {
	errs, err := fetchBrowseKindConn(ctx, conn, ftsQuery, taskType, "error_resolution", errLimit, full)
	if err != nil {
		return nil, err
	}
	patterns, err := fetchBrowseKindConn(ctx, conn, ftsQuery, taskType, "task_pattern", taskLimit, full)
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

func fetchBrowseKindConn(ctx context.Context, conn *sql.Conn, ftsQuery, taskType, kind string, limit int, full bool) ([]ContextMemory, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT m.id, m.kind, m.title, m.what, m.learned, m.created_at
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
		var what, learned string
		if err := rows.Scan(&m.ID, &m.Kind, &m.Title, &what, &learned, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan browse (%s): %w", kind, err)
		}
		m.Tier = "browse"
		if full {
			m.What = what
			m.Learned = learned
		} else {
			m.Snippet = snippet(what, browseSnippetChars)
		}
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
