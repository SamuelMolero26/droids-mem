package store

import (
	"context"
	"fmt"
	"strings"
)

// Tier sizes for the browse tier. Always-tier prioritizes the latest
// session_summary + newest user_rules; older rules surface as browse-tier title
// stubs until the encoded response budget is full. Explicit omission metadata
// keeps overflow discoverable through search instead of hiding it silently.
const (
	browseErrorLimit   = 10
	browseTaskLimit    = 10
	browseSnippetChars = 120
	// deep mode returns full bodies, ~100× a snippet, so its browse tier is
	// deliberately tighter than orient's to keep the bundle within the context
	// budget it exists to spend wisely (ADR-0012).
	deepErrorLimit = 5
	deepTaskLimit  = 5

	// MaxContextResponseBytes is the encoded transport ceiling for one
	// mem_context result. Store.Context stays below MaxContextPayloadBytes so
	// transport-specific session/help metadata can be added safely.
	MaxContextResponseBytes = 40_000
	MaxContextPayloadBytes  = 38_000
	contextSelectionBytes   = 37_500
	contextRuleCandidateCap = 512
)

// ContextMode selects retrieval depth for a Context bundle (ADR-0012).
type ContextMode string

const (
	ModeOrient  ContextMode = "orient"  // default: always tier + browse snippets
	ModeDeep    ContextMode = "deep"    // prioritize full bodies, then disclose stubs/omissions at the byte ceiling
	ModeRefresh ContextMode = "refresh" // always tier only — cheap mid-run re-anchor
)

type ContextRequest struct {
	TaskType string      `json:"task_type"`
	Query    string      `json:"query"`          // optional — absent or punctuation-only ranks browse by recency
	Mode     ContextMode `json:"mode,omitempty"` // optional — defaults to orient
}

// ContextMemory represents a full memory, a snippet, or a title-only stub. Tier
// identifies priority; the body fields identify how much detail fit.
type ContextMemory struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	Title          string `json:"title"`
	Tier           string `json:"tier"` // "always" | "browse"
	Learned        string `json:"learned,omitempty"`
	What           string `json:"what,omitempty"`
	Snippet        string `json:"snippet,omitempty"`
	CreatedAt      int64  `json:"created_at"`
	AuthoredAt     int64  `json:"authored_at"`
	ExpandCount    int    `json:"expand_count"`
	LastExpandedAt int64  `json:"last_expanded_at,omitempty"`
}

type ContextResponse struct {
	TaskType    string          `json:"task_type"`
	LastSession *ContextMemory  `json:"last_session,omitempty"`
	UserRules   []ContextMemory `json:"user_rules"`
	// UserRulesTotal counts every user_rule row for the task_type. Returned rules
	// may be full, stubbed, or included in OmittedByKind when the budget fills.
	UserRulesTotal int             `json:"user_rules_total"`
	Browse         []ContextMemory `json:"browse"`
	// StubbedCount counts returned memories without a complete body. Omitted
	// counts memories the selected mode would otherwise return but the encoded
	// response budget could not carry.
	StubbedCount  int            `json:"stubbed_count,omitempty"`
	OmittedCount  int            `json:"omitted_count,omitempty"`
	OmittedByKind map[string]int `json:"omitted_by_kind,omitempty"`
	Reason        string         `json:"reason,omitempty"`
	// Help carries contextual-disclosure hints (AXI §9) pointing at the
	// full-detail escape hatch for stub IDs. Never set by Store.Context
	// itself — the CLI/MCP boundary sets it when stubs or omissions need a
	// transport-appropriate recovery path, so this stays neutral here.
	Help []string `json:"help,omitempty"`
}

func (s *Store) Context(ctx context.Context, req ContextRequest) (*ContextResponse, error) {
	taskType := strings.ToLower(strings.TrimSpace(req.TaskType))
	if taskType == "" {
		return nil, &ValidationError{Field: "task_type", Message: "required", Retryable: true}
	}
	if !reTaskType.MatchString(taskType) {
		return nil, &ValidationError{
			Field:      "task_type",
			Message:    "must be a lowercase slug ^[a-z0-9][a-z0-9._-]*$ — no spaces, slashes, or '..'",
			Retryable:  true,
			Suggestion: "use the git repo or top-level dir name, e.g. 'droids-mem'",
		}
	}
	if err := enforceFieldCap("task_type", taskType, MaxTaskTypeLen, "use a short workflow slug, e.g. 'crm_upload'"); err != nil {
		return nil, err
	}
	if err := checkIdentifierForSecrets("task_type", taskType); err != nil {
		return nil, err
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

	// Empty (or punctuation-only) query routes the browse tier to its recency
	// branch. The task_type is deliberately NOT substituted in as a stand-in
	// search term: it is an identity key, not vocabulary (ADR-0032, issue #76).
	//
	// The hasSearchableText gate is load-bearing, not defensive: phraseFTSQuery
	// splits on strings.Fields, so punctuation-only input yields a non-empty
	// MATCH expression whose phrases tokenize to nothing and match no row —
	// silently reproducing the empty browse tier this branch exists to prevent.
	ftsQuery := ""
	if hasSearchableText(req.Query) {
		ftsQuery = phraseFTSQuery(strings.TrimSpace(req.Query))
	}

	// Metadata selection and selected-body hydration run inside a single BEGIN
	// DEFERRED on a dedicated connection so the returned bundle is a consistent
	// snapshot. Without
	// this, a concurrent writer could commit between phases, producing a bundle
	// that mixes pre- and post-write state (e.g. retention prune deleted the old
	// session_summary, new one
	// not yet committed → LastSession is stale or missing while browse-tier
	// rows reference the new session_id).
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire conn: %w", err)
	}
	defer conn.Close()
	var resp *ContextResponse
	err = withTx(ctx, conn, "DEFERRED", "context", func() error {
		var buildErr error
		resp, buildErr = buildContextResponseConn(ctx, conn, taskType, ftsQuery, mode)
		return buildErr
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// maxAlwaysTierUserRules bounds the always-tier user_rule slice so the
// context bundle payload stays within the PRD §3.2 budget. Locked decision
// #20, amended by ADR-0011: older rules are not silently dropped — they come
// back as browse-tier title stubs the agent can expand via mem_get.
const maxAlwaysTierUserRules = 5

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
