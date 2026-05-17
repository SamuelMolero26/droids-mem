package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

var validKinds = map[string]bool{
	"error_resolution": true,
	"task_pattern":     true,
	"user_rule":        true,
	"session_summary":  true,
}

var (
	rePunct      = regexp.MustCompile(`[^\w\s]`)
	reWhitespace = regexp.MustCompile(`\s+`)
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
	Tags      string `json:"tags"`  // space-delimited tokens
	Force     bool   `json:"force"` // HITL correction: overwrite matched fingerprint
}

type SaveResponse struct {
	Status    string  `json:"status"` // saved | skipped | updated
	ID        string  `json:"id,omitempty"`
	SessionID string  `json:"session_id,omitempty"`
	MatchedID string  `json:"matched_id,omitempty"` // present when skipped
	Reason    string  `json:"reason,omitempty"`     // duplicate | near_duplicate
	Score     float64 `json:"score,omitempty"`      // Jaccard similarity for near_duplicate
}

type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("field %q: %s", e.Field, e.Message)
}

func (s *Store) Save(req SaveRequest) (*SaveResponse, error) {
	if err := validate(&req); err != nil {
		return nil, err
	}

	sessionID := req.SessionID
	if sessionID == "" {
		sessionID = "sess_" + ulid.Make().String()
	}

	fp := fingerprint(req.TaskType, req.Kind, req.Title, req.Learned)
	now := time.Now().Unix()

	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire conn: %w", err)
	}
	defer conn.Close()

	// BEGIN IMMEDIATE acquires the write lock up front, closing the dedupe
	// race between fingerprint check + INSERT. busy_timeout (5s) absorbs
	// concurrent contention from other writers.
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, fmt.Errorf("begin immediate: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			conn.ExecContext(ctx, "ROLLBACK")
		}
	}()

	// layer 1: exact fingerprint match
	existingID, err := findByFingerprintConn(ctx, conn, fp)
	if err != nil {
		return nil, fmt.Errorf("fingerprint check: %w", err)
	}
	if existingID != "" {
		if req.Force {
			resp, err := forceUpdateConn(ctx, conn, existingID, sessionID, req, fp, now)
			if err != nil {
				return nil, err
			}
			if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
				return nil, fmt.Errorf("commit force update: %w", err)
			}
			committed = true
			return resp, nil
		}
		if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
			return nil, fmt.Errorf("commit skip: %w", err)
		}
		committed = true
		return &SaveResponse{
			Status:    "skipped",
			Reason:    "duplicate",
			MatchedID: existingID,
			SessionID: sessionID,
		}, nil
	}

	// layer 2: near-duplicate via BM25 top-K + Jaccard
	matchedID, similarity, err := nearDuplicateConn(ctx, conn, req)
	if err != nil {
		return nil, fmt.Errorf("near-duplicate check: %w", err)
	}
	if matchedID != "" {
		if req.Force {
			resp, err := forceUpdateConn(ctx, conn, matchedID, sessionID, req, fp, now)
			if err != nil {
				return nil, err
			}
			resp.Score = similarity
			if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
				return nil, fmt.Errorf("commit force update: %w", err)
			}
			committed = true
			return resp, nil
		}
		if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
			return nil, fmt.Errorf("commit skip: %w", err)
		}
		committed = true
		return &SaveResponse{
			Status:    "skipped",
			Reason:    "near_duplicate",
			MatchedID: matchedID,
			Score:     similarity,
			SessionID: sessionID,
		}, nil
	}

	id := "mem_" + ulid.Make().String()

	// UPSERT keeps insert idempotent against fingerprint races. With the
	// IMMEDIATE write lock held, conflict here is theoretically impossible —
	// kept as defense in depth.
	_, err = conn.ExecContext(ctx, `
		INSERT INTO memories (id, session_id, task_type, kind, title, what, learned, tags, fingerprint, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(fingerprint) DO UPDATE SET
			title       = excluded.title,
			what        = excluded.what,
			learned     = excluded.learned,
			tags        = excluded.tags,
			session_id  = excluded.session_id,
			updated_at  = excluded.updated_at
	`, id, sessionID, req.TaskType, req.Kind, req.Title, req.What, req.Learned, req.Tags, fp, now, now)
	if err != nil {
		return nil, fmt.Errorf("insert memory: %w", err)
	}

	// prune inside the txn so rolling-window cap is atomic with insert
	if req.Kind == "session_summary" {
		if err := pruneSessionSummariesConn(ctx, conn, req.TaskType); err != nil {
			return nil, fmt.Errorf("prune session summaries: %w", err)
		}
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, fmt.Errorf("commit save: %w", err)
	}
	committed = true

	return &SaveResponse{
		Status:    "saved",
		ID:        id,
		SessionID: sessionID,
	}, nil
}

const maxSessionSummaries = 5

func pruneSessionSummariesConn(ctx context.Context, conn *sql.Conn, taskType string) error {
	_, err := conn.ExecContext(ctx, `
		DELETE FROM memories
		WHERE task_type = ? AND kind = 'session_summary'
		AND id NOT IN (
			SELECT id FROM memories
			WHERE task_type = ? AND kind = 'session_summary'
			ORDER BY created_at DESC
			LIMIT ?
		)
	`, taskType, taskType, maxSessionSummaries)
	return err
}

func forceUpdateConn(ctx context.Context, conn *sql.Conn, existingID, sessionID string, req SaveRequest, fp string, now int64) (*SaveResponse, error) {
	_, err := conn.ExecContext(ctx, `
		UPDATE memories SET title=?, what=?, learned=?, tags=?, fingerprint=?, updated_at=?
		WHERE id=?
	`, req.Title, req.What, req.Learned, req.Tags, fp, now, existingID)
	if err != nil {
		return nil, fmt.Errorf("force update: %w", err)
	}
	return &SaveResponse{
		Status:    "updated",
		ID:        existingID,
		SessionID: sessionID,
	}, nil
}

// nearDuplicateConn pulls top-K BM25 candidates and re-ranks via Jaccard
// similarity over normalized token sets of (title+what+learned+tags).
// Returns the best match if similarity >= jaccardDupeThreshold.
func nearDuplicateConn(ctx context.Context, conn *sql.Conn, req SaveRequest) (matchedID string, similarity float64, err error) {
	terms := searchTerms(req.Title + " " + req.What + " " + req.Learned + " " + req.Tags)
	if len(terms) == 0 {
		return "", 0, nil
	}
	query := strings.Join(terms, " OR ")

	rows, err := conn.QueryContext(ctx, `
		SELECT m.id, m.title, m.what, m.learned, m.tags
		FROM memories_fts fts
		JOIN memories m ON m.rowid = fts.rowid
		WHERE memories_fts MATCH ?
		AND m.task_type = ?
		AND m.kind = ?
		ORDER BY bm25(memories_fts, 3, 1, 2, 1)
		LIMIT ?
	`, query, req.TaskType, req.Kind, bm25CandidateLimit)
	if err != nil {
		return "", 0, err
	}
	defer rows.Close()

	reqTokens := tokenSet(req.Title + " " + req.What + " " + req.Learned + " " + req.Tags)
	if len(reqTokens) == 0 {
		return "", 0, nil
	}

	bestID := ""
	bestSim := 0.0
	for rows.Next() {
		var id, title, what, learned, tags string
		if err := rows.Scan(&id, &title, &what, &learned, &tags); err != nil {
			return "", 0, err
		}
		candTokens := tokenSet(title + " " + what + " " + learned + " " + tags)
		sim := jaccard(reqTokens, candTokens)
		if sim > bestSim {
			bestSim = sim
			bestID = id
		}
	}
	if err := rows.Err(); err != nil {
		return "", 0, err
	}
	if bestSim >= jaccardDupeThreshold {
		return bestID, bestSim, nil
	}
	return "", 0, nil
}

func findByFingerprintConn(ctx context.Context, conn *sql.Conn, fp string) (string, error) {
	var id string
	err := conn.QueryRowContext(ctx, `SELECT id FROM memories WHERE fingerprint = ?`, fp).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

func validate(req *SaveRequest) error {
	req.TaskType = strings.ToLower(strings.TrimSpace(req.TaskType))
	if req.TaskType == "" {
		return &ValidationError{Field: "task_type", Message: "required"}
	}
	if !validKinds[req.Kind] {
		return &ValidationError{Field: "kind", Message: "must be one of: error_resolution, task_pattern, user_rule, session_summary"}
	}
	if strings.TrimSpace(req.Title) == "" {
		return &ValidationError{Field: "title", Message: "required"}
	}
	if strings.TrimSpace(req.What) == "" {
		return &ValidationError{Field: "what", Message: "required"}
	}
	if strings.TrimSpace(req.Learned) == "" {
		return &ValidationError{Field: "learned", Message: "required"}
	}
	// PII scrub hook — pass-through in V1, see internal/store/scrub.go
	req.Title = scrubPII(req.Title)
	req.What = scrubPII(req.What)
	req.Learned = scrubPII(req.Learned)
	req.Tags = scrubPII(req.Tags)
	return nil
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
