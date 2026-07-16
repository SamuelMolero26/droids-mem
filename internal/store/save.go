package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/samuelmolero26/droids-mem/internal/state"
)

var validKinds = map[string]bool{
	"error_resolution": true,
	"task_pattern":     true,
	"user_rule":        true,
	"session_summary":  true,
}

var validScopes = map[string]bool{
	"personal": true,
	"shared":   true,
}

var validOrigins = map[string]bool{
	"manual": true,
	"auto":   true,
}

// DefaultScope is what the save path stamps when the caller omits the scope
// field. Matches the column default in schema.go so behavior is consistent
// whether the row arrives through the API or a direct INSERT. 'personal' by
// default (ADR-0028): a memory never leaves the local store unless explicitly
// shared via `share` or --scope shared.
const DefaultScope = "personal"

// DefaultOrigin is stamped when the caller omits origin. 'auto' is reserved for
// the session-end enforcement path (ADR-0016); every explicit save is 'manual'.
const DefaultOrigin = "manual"

// Field caps (locked decision #8). Hard limits — any field exceeding its cap
// triggers a `field_too_large` rejection. Caps are forcing functions for
// distilled lessons; without them the worst-case storage budget (PRD §3.2)
// blows past the 25 MB target.
const (
	MaxTitleLen   = 200
	MaxWhatLen    = 8192
	MaxLearnedLen = 4096
	MaxTagsLen    = 500
	// MaxTaskTypeLen / MaxSessionIDLen bound the two free-form identifier
	// fields. Without caps they bypass the storage budget entirely (every
	// other field is capped) and bloat the FTS index via task_type. Real
	// values are short: sess_<ULID> = 31 chars, task types ≤ ~20.
	MaxTaskTypeLen  = 64
	MaxSessionIDLen = 64
)

var (
	// rePunct strips punctuation while keeping word chars, whitespace, and
	// hyphens intact. Decision #18 in the v1.0 plan: aligns the normalizer
	// with the FTS5 tokenizer (`unicode61 tokenchars=_-`) so the fingerprint,
	// BM25 query, and Jaccard token set all index the same atoms for
	// identifiers like `phone_number` and `field-mapping`. Existing rows had
	// their fingerprints rewritten by `migrate --rescrub`.
	rePunct      = regexp.MustCompile(`[^\w\s\-]`)
	reWhitespace = regexp.MustCompile(`\s+`)
	// reTaskType gates task_type at the save trust boundary (SEC-1). task_type
	// is a path segment on shared-pool export (FR-3: <repo>/<task_type>/shared.jsonl),
	// so an imported row with `/`, `..`, or a leading dot could escape the repo
	// root. Allow only a safe single dir/repo slug — nothing legitimate (repo or
	// top-level dir names) is rejected. Runs on local save AND pool import.
	reTaskType = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	// reRedactionToken matches the bracketed replacement tokens that Scrub
	// inserts. Used by isEmptyAfterScrub to decide whether the post-scrub
	// `learned` field is structurally empty.
	reRedactionToken = regexp.MustCompile(`\[[A-Z_]+\]`)
)

// jaccardDupeThreshold: token-set Jaccard similarity above this value
// classifies a candidate as a near-duplicate. Tuned for short jargon-dense
// memory text; revisit once corpora exceed ~10k memories.
const jaccardDupeThreshold = 0.85

// bm25CandidateLimit: number of top-BM25 rows pulled for second-pass
// Jaccard comparison. Wider than V0 (LIMIT 1) so corpus growth does not
// hide a true near-duplicate behind term-frequency noise.
const bm25CandidateLimit = 20

type SaveRequest struct {
	SessionID string `json:"session_id"` // optional — auto-generated if empty
	TaskType  string `json:"task_type"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	What      string `json:"what"`
	Learned   string `json:"learned"`
	Tags      string `json:"tags"`             // space-delimited tokens
	Scope     string `json:"scope,omitempty"`  // "personal" | "shared", defaults to "personal" (ADR-0028)
	Origin    string `json:"origin,omitempty"` // "manual" | "auto", defaults to "manual" (ADR-0016)
	Force     bool   `json:"force"`            // HITL correction: overwrite matched fingerprint
	DryRun    bool   `json:"dry_run"`          // run full pipeline (validate → scrub → dedupe) then ROLLBACK
	// Supersedes is the id of a Memory this save replaces (ADR-0018). Hard-deleted
	// in the same txn on successful insert, scope-bound so a mismatched/missing
	// target is a benign no-op. Empty = no supersession.
	Supersedes string `json:"supersedes,omitempty"`
}

type SaveResponse struct {
	Status         string       `json:"status"` // saved | skipped | updated
	ID             string       `json:"id,omitempty"`
	SessionID      string       `json:"session_id,omitempty"`
	MatchedID      string       `json:"matched_id,omitempty"`      // present when skipped
	MatchedTitle   string       `json:"matched_title,omitempty"`   // echo norm-setting wording on skip
	MatchedLearned string       `json:"matched_learned,omitempty"` // echo norm-setting wording on skip
	Reason         string       `json:"reason,omitempty"`          // duplicate | near_duplicate
	Score          float64      `json:"score,omitempty"`           // Jaccard similarity for near_duplicate
	Scrub          *ScrubReport `json:"scrub,omitempty"`           // present only when redactions occurred
	Superseded     string       `json:"superseded,omitempty"`      // id deleted via supersedes (ADR-0018); absent when target didn't match
}

// ValidationError is the structured error envelope returned for any save-path
// rejection. Required-field misses populate just Field + Message (Code empty
// signals a generic validation failure for backward compatibility); scrub +
// cap errors set Code and the richer metadata for agent self-correction.
type ValidationError struct {
	Code       string `json:"code,omitempty"`
	Field      string `json:"field,omitempty"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable"`
	Suggestion string `json:"suggestion,omitempty"`
	// Limit/Actual are set only on field_too_large, both in bytes.
	Limit           int          `json:"limit,omitempty"`
	Actual          int          `json:"actual,omitempty"`
	OffendingTags   []string     `json:"offending_tags,omitempty"`
	MatchedPatterns []string     `json:"matched_patterns,omitempty"`
	Scrub           *ScrubReport `json:"scrub,omitempty"`
}

func (e *ValidationError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("field %q: %s", e.Field, e.Message)
}

func (s *Store) Save(ctx context.Context, req SaveRequest) (*SaveResponse, error) {
	scrubReport, err := validate(&req)
	if err != nil {
		return nil, err
	}

	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = "sess_" + ulid.Make().String()
	}

	fp := fingerprint(req.TaskType, req.Kind, req.Title, req.Learned)
	now := time.Now().Unix()

	responseScrub := scrubReportForResponse(scrubReport)
	scrubCountsJSON, err := scrubCountsForStorage(scrubReport)
	if err != nil {
		return nil, fmt.Errorf("marshal scrub_counts: %w", err)
	}

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire conn: %w", err)
	}
	defer conn.Close()

	// BEGIN IMMEDIATE acquires the write lock up front, closing the dedupe
	// race between fingerprint check + INSERT. busy_timeout (5s) absorbs
	// concurrent contention from other writers. Dry-run rides the same lock
	// so its duplicate/near-duplicate prediction is exact, then rolls back.
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, fmt.Errorf("begin immediate: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			// Background ctx: the rollback must run even when the request
			// ctx is already canceled — that cancellation is often exactly
			// why we are rolling back.
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	// layer 1: exact fingerprint match
	existing, err := findByFingerprintConn(ctx, conn, fp)
	if err != nil {
		return nil, fmt.Errorf("fingerprint check: %w", err)
	}
	if existing != nil {
		if req.Force {
			resp, err := forceUpdateConn(ctx, conn, existing.ID, sessionID, req, fp, now, scrubCountsJSON)
			if err != nil {
				return nil, err
			}
			resp.Scrub = responseScrub
			if err := endTxn(ctx, conn, req.DryRun); err != nil {
				return nil, fmt.Errorf("commit force update: %w", err)
			}
			committed = true
			return resp, nil
		}
		if err := endTxn(ctx, conn, req.DryRun); err != nil {
			return nil, fmt.Errorf("commit skip: %w", err)
		}
		committed = true
		logSaveOutcome(req, "skipped_fingerprint", 0, existing.ID)
		return &SaveResponse{
			Status:         "skipped",
			Reason:         "duplicate",
			MatchedID:      existing.ID,
			MatchedTitle:   existing.Title,
			MatchedLearned: existing.Learned,
			SessionID:      sessionID,
			Scrub:          responseScrub,
		}, nil
	}

	// layer 2: near-duplicate via BM25 top-K + Jaccard
	matched, similarity, err := nearDuplicateConn(ctx, conn, req)
	if err != nil {
		return nil, fmt.Errorf("near-duplicate check: %w", err)
	}
	if matched != nil && similarity >= jaccardDupeThreshold {
		if req.Force {
			resp, err := forceUpdateConn(ctx, conn, matched.ID, sessionID, req, fp, now, scrubCountsJSON)
			if err != nil {
				return nil, err
			}
			resp.Score = similarity
			resp.Scrub = responseScrub
			if err := endTxn(ctx, conn, req.DryRun); err != nil {
				return nil, fmt.Errorf("commit force update: %w", err)
			}
			committed = true
			return resp, nil
		}
		if err := endTxn(ctx, conn, req.DryRun); err != nil {
			return nil, fmt.Errorf("commit skip: %w", err)
		}
		committed = true
		logSaveOutcome(req, "skipped_jaccard", similarity, matched.ID)
		return &SaveResponse{
			Status:         "skipped",
			Reason:         "near_duplicate",
			MatchedID:      matched.ID,
			MatchedTitle:   matched.Title,
			MatchedLearned: matched.Learned,
			Score:          similarity,
			SessionID:      sessionID,
			Scrub:          responseScrub,
		}, nil
	}

	id := "mem_" + ulid.Make().String()

	// UPSERT keeps insert idempotent against fingerprint races. With the
	// IMMEDIATE write lock held, conflict here is theoretically impossible —
	// kept as defense in depth. Scope + scrub_pattern_version + scrub_counts
	// are stamped on every insert and refreshed on conflict so the row's
	// scrub provenance always matches the most recent body.
	// origin is intentionally NOT in the ON CONFLICT SET: a fingerprint
	// collision overwrites the body/provenance-of-scrub but preserves how the
	// existing row was originally authored.
	_, err = conn.ExecContext(ctx, `
		INSERT INTO memories
			(id, session_id, task_type, kind, title, what, learned, tags, fingerprint,
			 created_at, updated_at, scope, scrub_pattern_version, scrub_counts, origin)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(fingerprint) DO UPDATE SET
			title                 = excluded.title,
			what                  = excluded.what,
			learned               = excluded.learned,
			tags                  = excluded.tags,
			session_id            = excluded.session_id,
			updated_at            = excluded.updated_at,
			scope                 = excluded.scope,
			scrub_pattern_version = excluded.scrub_pattern_version,
			scrub_counts          = excluded.scrub_counts
	`, id, sessionID, req.TaskType, req.Kind, req.Title, req.What, req.Learned, req.Tags, fp,
		now, now, req.Scope, ScrubPatternVersion, scrubCountsJSON, req.Origin)
	if err != nil {
		return nil, fmt.Errorf("insert memory: %w", err)
	}

	// Supersede (ADR-0018): hard-delete the named target in the same txn, only
	// after a successful insert. The WHERE is scope-bound to the NEW memory's
	// kind/task_type/scope, so a mismatched or missing target matches 0 rows —
	// cross-boundary deletion is impossible by construction, no validate branch
	// needed. Runs on the INSERT path only; a force-update (fingerprint twin)
	// returns earlier and is its own replacement.
	var supersededID string
	if req.Supersedes != "" {
		res, err := conn.ExecContext(ctx,
			`DELETE FROM memories WHERE id=? AND kind=? AND task_type=? AND scope=?`,
			req.Supersedes, req.Kind, req.TaskType, req.Scope)
		if err != nil {
			return nil, fmt.Errorf("supersede delete: %w", err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			supersededID = req.Supersedes
		}
	}

	// prune inside the txn so the rolling-window cap is atomic with insert.
	// session_summary retention splits by origin (ADR-0016): manual summaries
	// keep newest-5 per task_type; auto summaries keep a global newest-M budget
	// evicted value-aware by the Expand signal.
	if req.Kind == "session_summary" {
		if req.Origin == "auto" {
			if err := pruneAutoSummariesConn(ctx, conn); err != nil {
				return nil, fmt.Errorf("prune auto summaries: %w", err)
			}
		} else if err := pruneSessionSummariesConn(ctx, conn, req.TaskType); err != nil {
			return nil, fmt.Errorf("prune session summaries: %w", err)
		}
	}

	if err := endTxn(ctx, conn, req.DryRun); err != nil {
		return nil, fmt.Errorf("commit save: %w", err)
	}
	committed = true

	nearMissID := ""
	if matched != nil {
		nearMissID = matched.ID
	}
	logSaveOutcome(req, "inserted", similarity, nearMissID)

	return &SaveResponse{
		Status:     "saved",
		ID:         id,
		SessionID:  sessionID,
		Scrub:      responseScrub,
		Superseded: supersededID,
	}, nil
}

// saveTuningRecord is one line of the threshold-tuning dataset (ADR-0026). ids
// and scalars only — no memory body — so the trust boundary matches mem.db and
// no scrub is needed. jaccard is omitted on the fingerprint path; the outcome
// field disambiguates a genuine zero-similarity insert from that omission.
type saveTuningRecord struct {
	Ev       string  `json:"ev"`
	Ts       int64   `json:"ts"`
	Kind     string  `json:"kind"`
	TaskType string  `json:"task_type"`
	Outcome  string  `json:"outcome"` // inserted | skipped_fingerprint | skipped_jaccard
	Jaccard  float64 `json:"jaccard,omitempty"`
	Matched  string  `json:"matched,omitempty"`
}

// logSaveOutcome records the dedupe decision for one save. Dry runs are skipped
// — they roll back and never persist, so they are predictions, not events.
func logSaveOutcome(req SaveRequest, outcome string, jaccard float64, matched string) {
	if req.DryRun {
		return
	}
	state.AppendTuningLog(saveTuningRecord{
		Ev:       "save",
		Ts:       time.Now().Unix(),
		Kind:     req.Kind,
		TaskType: req.TaskType,
		Outcome:  outcome,
		Jaccard:  jaccard,
		Matched:  matched,
	})
}

// endTxn finishes the save transaction: COMMIT normally, ROLLBACK when the
// caller asked for a dry run. Dry-run thereby exercises the exact production
// path (locks, dedupe, scrub, insert, prune) without persisting anything.
func endTxn(ctx context.Context, conn *sql.Conn, dryRun bool) error {
	stmt := "COMMIT"
	if dryRun {
		stmt = "ROLLBACK"
	}
	_, err := conn.ExecContext(ctx, stmt)
	return err
}

const maxSessionSummaries = 5

// maxAutoSummaries (M) is the global budget for origin='auto' session summaries;
// autoSummaryGrace (K) is the cold-start window protected from value-eviction.
// K < M is required (ADR-0016 pt 6). Hardcoded constants for v1, consistent with
// the hardcoded context-tier sizes.
const (
	maxAutoSummaries = 30
	autoSummaryGrace = 5
)

// pruneSessionSummariesConn enforces the per-task_type newest-5 cap for MANUAL
// session summaries. Scoped to origin='manual' so it never evicts an auto
// summary that happens to share a task_type bucket.
func pruneSessionSummariesConn(ctx context.Context, conn *sql.Conn, taskType string) error {
	_, err := conn.ExecContext(ctx, `
		DELETE FROM memories
		WHERE task_type = ? AND kind = 'session_summary' AND origin = 'manual'
		AND id NOT IN (
			SELECT id FROM memories
			WHERE task_type = ? AND kind = 'session_summary' AND origin = 'manual'
			ORDER BY created_at DESC
			LIMIT ?
		)
	`, taskType, taskType, maxSessionSummaries)
	return err
}

// pruneAutoSummariesConn enforces the global newest-M budget for origin='auto'
// session summaries, evicting value-aware via the Expand signal (ADR-0016 pt 6).
// The keep set is the M most-keepable autos ranked, highest first, by:
//  1. inside the newest-K by created_at (cold-start grace — always kept),
//  2. ever-expanded over never-expanded (proven value),
//  3. warmer last_expanded_at (LRU; SQLite sorts NULL last under DESC),
//  4. newer created_at.
//
// Everything outside that set is deleted — which reproduces the eviction
// precedence: never-expanded-outside-K-oldest dies first, then
// expanded-outside-K-coldest, then (only if all rows sit inside K) oldest
// overall. M is the hard cap; K is a shield, not a veto.
func pruneAutoSummariesConn(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, `
		DELETE FROM memories
		WHERE kind = 'session_summary' AND origin = 'auto'
		AND id NOT IN (
			SELECT id FROM memories
			WHERE kind = 'session_summary' AND origin = 'auto'
			ORDER BY
				(CASE WHEN id IN (
					SELECT id FROM memories
					WHERE kind = 'session_summary' AND origin = 'auto'
					ORDER BY created_at DESC
					LIMIT ?
				) THEN 1 ELSE 0 END) DESC,
				(CASE WHEN expand_count > 0 THEN 1 ELSE 0 END) DESC,
				last_expanded_at DESC,
				created_at DESC
			LIMIT ?
		)
	`, autoSummaryGrace, maxAutoSummaries)
	return err
}

func forceUpdateConn(ctx context.Context, conn *sql.Conn, existingID, sessionID string, req SaveRequest, fp string, now int64, scrubCountsJSON sql.NullString) (*SaveResponse, error) {
	_, err := conn.ExecContext(ctx, `
		UPDATE memories
		SET title=?, what=?, learned=?, tags=?, fingerprint=?, updated_at=?,
		    scope=?, scrub_pattern_version=?, scrub_counts=?
		WHERE id=?
	`, req.Title, req.What, req.Learned, req.Tags, fp, now,
		req.Scope, ScrubPatternVersion, scrubCountsJSON, existingID)
	if err != nil {
		return nil, fmt.Errorf("force update: %w", err)
	}
	return &SaveResponse{
		Status:    "updated",
		ID:        existingID,
		SessionID: sessionID,
	}, nil
}

// matchedRow is the projection returned by both dedupe layers so skip
// responses can echo back the matched row's title and learned body —
// soft norm-setting toward the corpus's preferred wording (decision #9).
type matchedRow struct {
	ID      string
	Title   string
	Learned string
}

// bm25QueryTermCap caps the OR-arity of the BM25 query string built by
// nearDuplicateConn. Locked decision #19: at the 8 KB `what` cap a single row
// can produce 1000+ unique terms which slows search p95 well past the
// 100 ms target. 100 highest-IDF (= longest) terms preserve the duplicate
// signal at a fixed query cost.
const bm25QueryTermCap = 100

// nearDuplicateConn pulls top-K BM25 candidates and re-ranks via Jaccard
// similarity over normalized token sets of (title+what+learned+tags). Returns
// the single best candidate and its similarity, or (nil, 0) when no BM25
// candidate matched. The caller applies jaccardDupeThreshold — the best
// similarity is surfaced even on the insert path so near-misses can be logged
// for threshold tuning (ADR-0026).
func nearDuplicateConn(ctx context.Context, conn *sql.Conn, req SaveRequest) (*matchedRow, float64, error) {
	body := req.Title + " " + req.What + " " + req.Learned + " " + req.Tags
	terms, reqTokens := dedupeTokens(body)
	if len(terms) == 0 {
		return nil, 0, nil
	}
	// Cap query arity (decision #19).
	if len(terms) > bm25QueryTermCap {
		sortTermsByIDF(terms)
		terms = terms[:bm25QueryTermCap]
	}
	// Wrap each term as an FTS5 phrase literal ("term") so that any FTS5
	// operator keywords (NOT/AND/OR/NEAR, col:filter) that happen to appear
	// in saved Memory content are treated as literal tokens, not query
	// operators. searchTerms() already lowercases + strips punctuation, but
	// not operator keywords — quoting closes that gap.
	// Threat model differs from search.go (caller-supplied query, operators
	// intentional). See ADR-0003.
	quoted := make([]string, len(terms))
	for i, t := range terms {
		quoted[i] = `"` + t + `"`
	}
	query := strings.Join(quoted, " OR ")

	// m.id != ? exempts the supersedes target (ADR-0018): a replacement resembles
	// what it replaces, so the target is the row most likely to trip the Jaccard
	// gate and self-defeatingly skip the save. Empty Supersedes never matches a
	// real mem_ id, so the clause is a no-op on ordinary saves.
	rows, err := conn.QueryContext(ctx, `
		SELECT m.id, m.title, m.what, m.learned, m.tags
		FROM memories_fts fts
		JOIN memories m ON m.rowid = fts.rowid
		WHERE memories_fts MATCH ?
		AND m.task_type = ?
		AND m.kind = ?
		AND m.id != ?
		ORDER BY bm25(memories_fts, 3, 1, 2, 1)
		LIMIT ?
	`, query, req.TaskType, req.Kind, req.Supersedes, bm25CandidateLimit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var best matchedRow
	bestSim := 0.0
	for rows.Next() {
		var id, title, what, learned, tags string
		if err := rows.Scan(&id, &title, &what, &learned, &tags); err != nil {
			return nil, 0, err
		}
		candTokens := tokenSet(title + " " + what + " " + learned + " " + tags)
		sim := jaccard(reqTokens, candTokens)
		if sim > bestSim {
			bestSim = sim
			best = matchedRow{ID: id, Title: title, Learned: learned}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if best.ID == "" {
		// No BM25 candidate matched — no near neighbour to report.
		return nil, 0, nil
	}
	return &best, bestSim, nil
}

func findByFingerprintConn(ctx context.Context, conn *sql.Conn, fp string) (*matchedRow, error) {
	var row matchedRow
	err := conn.QueryRowContext(ctx,
		`SELECT id, title, learned FROM memories WHERE fingerprint = ?`, fp,
	).Scan(&row.ID, &row.Title, &row.Learned)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// validate runs the v1.0 save-path gate sequence (locked plan §Phase 3):
//  1. Required fields present.
//  2. Scope default + allowed-value check.
//  3. Field caps.
//  4. Tag pre-scrub (tags rejected, never redacted).
//  5. Trim trailing whitespace.
//  6. Scrub title/what/learned.
//  7. Empty-after-scrub check on `learned`.
//
// Returns the aggregated ScrubReport across all three text fields. nil when
// nothing was redacted; callers may still receive a non-nil report with
// RedactionCount == 0 (no fields fired) for code-path uniformity.
func validate(req *SaveRequest) (*ScrubReport, error) {
	req.TaskType = strings.ToLower(strings.TrimSpace(req.TaskType))
	if req.TaskType == "" {
		return nil, &ValidationError{Field: "task_type", Message: "required", Retryable: true}
	}
	// SEC-1: task_type crosses to the filesystem as a path segment on shared
	// export; reject anything that isn't a safe single slug before it is stored.
	if !reTaskType.MatchString(req.TaskType) {
		return nil, &ValidationError{
			Field:      "task_type",
			Message:    "must match ^[a-z0-9][a-z0-9._-]*$ (a repo/dir slug)",
			Retryable:  true,
			Suggestion: "use a short slug like 'crm_upload'; no slashes, path traversal, or leading dot",
		}
	}
	if !validKinds[req.Kind] {
		return nil, &ValidationError{
			Field:     "kind",
			Message:   "must be one of: error_resolution, task_pattern, user_rule, session_summary",
			Retryable: true,
		}
	}
	if strings.TrimSpace(req.Title) == "" {
		return nil, &ValidationError{Field: "title", Message: "required", Retryable: true}
	}
	if strings.TrimSpace(req.What) == "" {
		return nil, &ValidationError{Field: "what", Message: "required", Retryable: true}
	}
	if strings.TrimSpace(req.Learned) == "" {
		return nil, &ValidationError{Field: "learned", Message: "required", Retryable: true}
	}

	if req.Scope == "" {
		req.Scope = DefaultScope
	}
	if !validScopes[req.Scope] {
		return nil, &ValidationError{
			Field:      "scope",
			Message:    "must be 'personal' or 'shared'",
			Retryable:  true,
			Suggestion: "omit scope to use the default ('shared')",
		}
	}

	if req.Origin == "" {
		req.Origin = DefaultOrigin
	}
	if !validOrigins[req.Origin] {
		return nil, &ValidationError{
			Field:      "origin",
			Message:    "must be 'manual' or 'auto'",
			Retryable:  true,
			Suggestion: "omit origin to use the default ('manual')",
		}
	}

	if err := enforceFieldCap("task_type", req.TaskType, MaxTaskTypeLen, "use a short workflow slug, e.g. 'crm_upload'"); err != nil {
		return nil, err
	}
	if err := enforceFieldCap("session_id", req.SessionID, MaxSessionIDLen, "pass the session_id minted by mem_context, or omit it"); err != nil {
		return nil, err
	}
	if err := enforceFieldCap("title", req.Title, MaxTitleLen, "shorten the title to under the cap"); err != nil {
		return nil, err
	}
	if err := enforceFieldCap("what", req.What, MaxWhatLen, "split or condense the context body"); err != nil {
		return nil, err
	}
	if err := enforceFieldCap("learned", req.Learned, MaxLearnedLen, "distill the lesson to its essential insight"); err != nil {
		return nil, err
	}
	if err := enforceFieldCap("tags", req.Tags, MaxTagsLen, "drop redundant tags or shorten existing ones"); err != nil {
		return nil, err
	}

	if err := checkTagsForSecrets(req.Tags); err != nil {
		return nil, err
	}
	// Identifier fields are persisted verbatim (never redacted) and surface
	// in FTS + context bundles, so a secret in them is as bad as one in tags.
	// Same strict-reject policy: the agent rewrites, nothing is auto-stripped.
	if err := checkIdentifierForSecrets("task_type", req.TaskType); err != nil {
		return nil, err
	}
	if err := checkIdentifierForSecrets("session_id", req.SessionID); err != nil {
		return nil, err
	}

	req.Title = strings.TrimSpace(req.Title)
	req.What = strings.TrimSpace(req.What)
	req.Learned = strings.TrimSpace(req.Learned)
	req.Tags = strings.TrimSpace(req.Tags)

	titleOut, titleRep := Scrub(req.Title)
	whatOut, whatRep := Scrub(req.What)
	learnedOut, learnedRep := Scrub(req.Learned)
	req.Title = titleOut
	req.What = whatOut
	req.Learned = learnedOut

	agg := aggregateScrubReports(titleRep, whatRep, learnedRep)

	if isEmptyAfterScrub(req.Learned) {
		// Empty-after-scrub error always carries the scrub block so the
		// agent can see which pattern ate the body and reframe the lesson.
		return nil, &ValidationError{
			Code:       "scrub_emptied_learned",
			Field:      "learned",
			Message:    "scrub removed all content from learned",
			Retryable:  false,
			Suggestion: "rewrite the lesson without raw secrets; describe the pattern instead of quoting it",
			Scrub:      &agg,
		}
	}

	return &agg, nil
}

// enforceFieldCap measures bytes, not runes — caps protect the byte-denominated
// storage budget (PRD §3.2), and CONTEXT.md fixes "Field cap" as a byte limit.
func enforceFieldCap(field, value string, max int, suggestion string) error {
	if len(value) <= max {
		return nil
	}
	return &ValidationError{
		Code:       "field_too_large",
		Field:      field,
		Message:    fmt.Sprintf("max %d bytes (got %d)", max, len(value)),
		Limit:      max,
		Actual:     len(value),
		Retryable:  true,
		Suggestion: suggestion,
	}
}

// checkTagsForSecrets runs Scrub on each whitespace-delimited tag and rejects
// the save if any tag would have been redacted. Tags surface in search
// results untransformed, so a leaked secret in a tag is just as bad as one
// in `learned` — and we don't auto-strip in v1.0 (decision #13).
func checkTagsForSecrets(tags string) error {
	if tags == "" {
		return nil
	}
	// Fast path: one Scrub over the whole tags string. Clean tags (the common
	// case) cost a single detector pass instead of one per token. Scrubbing the
	// joined string can only detect a superset of the per-token hits — more
	// surrounding context never suppresses a match — so a zero here guarantees
	// every individual tag is clean.
	if _, whole := Scrub(tags); whole.RedactionCount == 0 {
		return nil
	}
	// A redaction fired. Re-scrub per token to name the offenders for the
	// diagnostic error. This path is rare (a real leak) and not latency-bound.
	tokens := strings.Fields(tags)
	var offending []string
	matched := map[string]struct{}{}
	for _, tok := range tokens {
		_, rep := Scrub(tok)
		if rep.RedactionCount == 0 {
			continue
		}
		offending = append(offending, tok)
		for name := range rep.PerPatternCounts {
			matched[name] = struct{}{}
		}
	}
	// Fallback: the whole-string match spanned a token boundary and couldn't be
	// localized. Report the whole-string patterns so the save is still rejected
	// rather than silently passing.
	if len(matched) == 0 {
		_, whole := Scrub(tags)
		for name := range whole.PerPatternCounts {
			matched[name] = struct{}{}
		}
	}
	patterns := make([]string, 0, len(matched))
	for n := range matched {
		patterns = append(patterns, n)
	}
	sort.Strings(patterns)
	return &ValidationError{
		Code:            "tag_contains_secret",
		Field:           "tags",
		Message:         "one or more tags contain redactable content",
		Retryable:       true,
		Suggestion:      "remove sensitive tokens from tags; tags are stored unscrubbed for search",
		OffendingTags:   offending,
		MatchedPatterns: patterns,
	}
}

// checkIdentifierForSecrets strict-rejects a save whose identifier field
// (task_type, session_id) matches any scrub detector. Identifiers are stored
// unscrubbed by design — they are routing keys, not prose — so redaction is
// not an option; rejection with a retryable error is.
func checkIdentifierForSecrets(field, value string) error {
	if value == "" {
		return nil
	}
	_, rep := Scrub(value)
	if rep.RedactionCount == 0 {
		return nil
	}
	patterns := make([]string, 0, len(rep.PerPatternCounts))
	for name := range rep.PerPatternCounts {
		patterns = append(patterns, name)
	}
	sort.Strings(patterns)
	return &ValidationError{
		Code:            field + "_contains_secret",
		Field:           field,
		Message:         "value matches a scrub detector and identifiers are stored unscrubbed",
		Retryable:       true,
		Suggestion:      "use a short non-sensitive slug for " + field,
		MatchedPatterns: patterns,
	}
}

// aggregateScrubReports folds the per-field reports into one row-level
// summary. FieldsRedacted lists fields with at least one redaction in stable
// (title, what, learned) order so callers see deterministic output.
func aggregateScrubReports(title, what, learned ScrubReport) ScrubReport {
	agg := ScrubReport{
		PerPatternCounts: map[string]int{},
		PatternVersion:   ScrubPatternVersion,
	}
	fold := func(field string, r ScrubReport) {
		if r.RedactionCount == 0 {
			return
		}
		agg.RedactionCount += r.RedactionCount
		for name, n := range r.PerPatternCounts {
			agg.PerPatternCounts[name] += n
		}
		agg.FieldsRedacted = append(agg.FieldsRedacted, field)
	}
	fold("title", title)
	fold("what", what)
	fold("learned", learned)
	return agg
}

// isEmptyAfterScrub reports whether the post-scrub `learned` value has no
// non-redaction, non-whitespace characters left. Backs decision #5 — refuse
// to persist a lesson that's been reduced to nothing but token markers.
func isEmptyAfterScrub(s string) bool {
	stripped := reRedactionToken.ReplaceAllString(s, "")
	return strings.TrimSpace(stripped) == ""
}

// scrubReportForResponse returns a pointer suitable for the response envelope
// per decision #7: only emitted when at least one redaction fired.
func scrubReportForResponse(agg *ScrubReport) *ScrubReport {
	if agg == nil || agg.RedactionCount == 0 {
		return nil
	}
	copy := *agg
	return &copy
}

// scrubCountsForStorage marshals the per-row scrub_counts JSON column. NULL
// when no redactions fired so the column stays sparse and `doctor
// --scrub-stats` can filter via json_extract IS NOT NULL.
func scrubCountsForStorage(agg *ScrubReport) (sql.NullString, error) {
	if agg == nil || agg.RedactionCount == 0 {
		return sql.NullString{}, nil
	}
	b, err := json.Marshal(agg)
	if err != nil {
		return sql.NullString{}, err
	}
	return sql.NullString{String: string(b), Valid: true}, nil
}

// fingerprint produces a deterministic hash from normalized fields.
// Pipeline: lowercase → strip punctuation → collapse whitespace → sort words → SHA256.
// Excludes `what` by design — see docs/adr/0001-fingerprint-excludes-what.md.
func fingerprint(taskType, kind, title, learned string) string {
	normalized := normalizeForFP(taskType + " " + kind + " " + title + " " + learned)
	h := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(h[:])
}

func normalizeForFP(s string) string {
	s = strings.ToLower(s)
	s = rePunct.ReplaceAllString(s, "")
	s = reWhitespace.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	words := strings.Fields(s)
	sort.Strings(words)
	return strings.Join(words, " ")
}

// sortTermsByIDF orders terms longest-first (length proxies IDF) so capping
// keeps the highest-signal tokens; alphabetical tie-break for stable runs.
func sortTermsByIDF(terms []string) {
	sort.Slice(terms, func(a, b int) bool {
		if len(terms[a]) != len(terms[b]) {
			return len(terms[a]) > len(terms[b])
		}
		return terms[a] < terms[b]
	})
}

// dedupeTokens normalizes body once and returns both the ordered unique term
// slice (for the BM25 candidate query) and the token set (for Jaccard scoring).
// Folds what searchTerms + tokenSet previously did in two passes — each
// re-lowercased and re-scanned the full ~13 KB concatenated body — into a
// single ToLower → strip-punct → collapse-whitespace → Fields sweep.
//
// Punctuation splits tokens (replaced with a space) so the BM25 query and the
// Jaccard set index the same atoms the FTS5 unicode61 tokenizer produces —
// the alignment decision #18 mandates. Tokens of len <= 2 are dropped as noise,
// matching both source functions.
func dedupeTokens(body string) ([]string, map[string]struct{}) {
	body = strings.ToLower(body)
	body = rePunct.ReplaceAllString(body, " ")
	body = reWhitespace.ReplaceAllString(body, " ")
	words := strings.Fields(strings.TrimSpace(body))
	set := make(map[string]struct{}, len(words))
	terms := make([]string, 0, len(words))
	for _, w := range words {
		if len(w) <= 2 {
			continue
		}
		if _, ok := set[w]; ok {
			continue
		}
		set[w] = struct{}{}
		terms = append(terms, w)
	}
	return terms, set
}

// searchTerms extracts unique lowercase words (len > 2) for BM25 queries.
func searchTerms(s string) []string {
	s = strings.ToLower(s)
	s = rePunct.ReplaceAllString(s, "")
	s = reWhitespace.ReplaceAllString(s, " ")
	words := strings.Fields(strings.TrimSpace(s))
	seen := make(map[string]bool, len(words))
	result := make([]string, 0, len(words))
	for _, w := range words {
		if len(w) > 2 && !seen[w] {
			seen[w] = true
			result = append(result, w)
		}
	}
	return result
}

// tokenSet builds the deduplicated set of meaningful tokens used for
// Jaccard similarity. Excludes 1-2 char tokens (noise).
func tokenSet(s string) map[string]struct{} {
	s = strings.ToLower(s)
	s = rePunct.ReplaceAllString(s, " ")
	s = reWhitespace.ReplaceAllString(s, " ")
	words := strings.Fields(strings.TrimSpace(s))
	set := make(map[string]struct{}, len(words))
	for _, w := range words {
		if len(w) > 2 {
			set[w] = struct{}{}
		}
	}
	return set
}

// jaccard returns |A ∩ B| / |A ∪ B|.
func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if _, ok := b[k]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}
