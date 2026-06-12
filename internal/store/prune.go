package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Prune implements the ADR-0010 manual deletion workflow. The system never
// deletes knowledge-kind memories on its own; this is the explicit,
// human-initiated path, dry-run unless Apply is set.

type PruneRequest struct {
	Kind          string `json:"kind,omitempty"`
	TaskType      string `json:"task_type,omitempty"`
	OlderThanDays int    `json:"older_than_days,omitempty"`
	Apply         bool   `json:"apply"`
}

type PrunedMemory struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	TaskType  string `json:"task_type"`
	Title     string `json:"title"`
	CreatedAt int64  `json:"created_at"`
}

type PruneResponse struct {
	Status  string         `json:"status"` // "dry_run" | "pruned"
	Count   int            `json:"count"`
	Matched []PrunedMemory `json:"matched"`
}

func (s *Store) Prune(ctx context.Context, req PruneRequest) (*PruneResponse, error) {
	req.Kind = strings.ToLower(strings.TrimSpace(req.Kind))
	req.TaskType = strings.ToLower(strings.TrimSpace(req.TaskType))

	if req.Kind == "" && req.TaskType == "" && req.OlderThanDays <= 0 {
		return nil, &ValidationError{
			Code:       "prune_unfiltered",
			Message:    "refusing to prune the entire database",
			Retryable:  true,
			Suggestion: "pass at least one of --kind, --task-type, --older-than-days",
		}
	}
	if req.Kind != "" && !validKinds[req.Kind] {
		return nil, &ValidationError{
			Code:      "invalid_kind",
			Field:     "kind",
			Message:   "must be one of: error_resolution, task_pattern, user_rule, session_summary",
			Retryable: true,
		}
	}

	where, args := pruneFilter(req)

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire conn: %w", err)
	}
	defer conn.Close()

	// IMMEDIATE so the matched set cannot drift between SELECT and DELETE.
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, fmt.Errorf("begin immediate: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	//nolint:gosec // G202: where built from hardcoded column names ("kind = ?", "task_type = ?", "created_at < ?"); args parameterized
	selectSQL := `SELECT id, kind, task_type, title, created_at FROM memories WHERE ` + where + ` ORDER BY created_at, id`
	rows, err := conn.QueryContext(ctx, selectSQL, args...)
	if err != nil {
		return nil, fmt.Errorf("select prune candidates: %w", err)
	}
	defer rows.Close()
	matched := []PrunedMemory{}
	for rows.Next() {
		var m PrunedMemory
		if err := rows.Scan(&m.ID, &m.Kind, &m.TaskType, &m.Title, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan prune candidate: %w", err)
		}
		matched = append(matched, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate prune candidates: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close prune candidates: %w", err)
	}

	resp := &PruneResponse{Status: "dry_run", Count: len(matched), Matched: matched}
	if req.Apply {
		// FTS stays in sync via the AD trigger — never touch memories_fts here.
		//nolint:gosec // G202: where built from hardcoded column names; args parameterized
		deleteSQL := `DELETE FROM memories WHERE ` + where
		if _, err := conn.ExecContext(ctx, deleteSQL, args...); err != nil {
			return nil, fmt.Errorf("delete pruned rows: %w", err)
		}
		resp.Status = "pruned"
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, fmt.Errorf("commit prune: %w", err)
	}
	committed = true
	return resp, nil
}

func pruneFilter(req PruneRequest) (string, []any) {
	conds := []string{}
	args := []any{}
	if req.Kind != "" {
		conds = append(conds, "kind = ?")
		args = append(args, req.Kind)
	}
	if req.TaskType != "" {
		conds = append(conds, "task_type = ?")
		args = append(args, req.TaskType)
	}
	if req.OlderThanDays > 0 {
		cutoff := time.Now().Unix() - int64(req.OlderThanDays)*86400
		conds = append(conds, "created_at < ?")
		args = append(args, cutoff)
	}
	return strings.Join(conds, " AND "), args
}

// ---------- suggest-dupes (ADR-0010) ----------

// DefaultSuggestThreshold relaxes the save-time jaccardDupeThreshold (0.85)
// for offline cluster discovery: a save-time false positive silently loses a
// save, a suggestion false positive costs a human a glance.
const DefaultSuggestThreshold = 0.6

type DupeMember struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Kind      string  `json:"kind"`
	TaskType  string  `json:"task_type"`
	CreatedAt int64   `json:"created_at"`
	Score     float64 `json:"score"` // Jaccard similarity to the cluster seed; 1 for the seed itself
}

type DupeCluster struct {
	SeedID  string       `json:"seed_id"`
	Members []DupeMember `json:"members"`
}

type SuggestDupesRequest struct {
	Kind      string  `json:"kind,omitempty"`      // optional scan narrowing
	TaskType  string  `json:"task_type,omitempty"` // optional scan narrowing
	Threshold float64 `json:"threshold"`
}

type SuggestDupesResponse struct {
	Status      string        `json:"status"`
	Threshold   float64       `json:"threshold"`
	RowsScanned int           `json:"rows_scanned"`
	Clusters    []DupeCluster `json:"clusters"`
}

type dupeRow struct {
	id, kind, taskType, title string
	text                      string
	createdAt                 int64
}

// SuggestDupes walks the corpus and emits clusters of likely-duplicate
// memories using the save-time dedupe mechanism (FTS5 BM25 top-K → Jaccard)
// at a relaxed threshold. Greedy consumed-set clustering: each unconsumed row
// seeds one cluster and every member leaves the pool, so denser duplicates
// mean fewer FTS queries. Read-only — suggestions feed a manual Prune.
func (s *Store) SuggestDupes(ctx context.Context, req SuggestDupesRequest) (*SuggestDupesResponse, error) {
	req.Kind = strings.ToLower(strings.TrimSpace(req.Kind))
	req.TaskType = strings.ToLower(strings.TrimSpace(req.TaskType))
	if req.Threshold <= 0 {
		req.Threshold = DefaultSuggestThreshold
	}
	if req.Threshold > 1 {
		return nil, &ValidationError{
			Code:      "invalid_threshold",
			Field:     "threshold",
			Message:   "must be in (0, 1]",
			Retryable: true,
		}
	}
	if req.Kind != "" && !validKinds[req.Kind] {
		return nil, &ValidationError{
			Code:      "invalid_kind",
			Field:     "kind",
			Message:   "must be one of: error_resolution, task_pattern, user_rule, session_summary",
			Retryable: true,
		}
	}

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire conn: %w", err)
	}
	defer conn.Close()

	// Single read transaction: one snapshot for the whole walk (clusters
	// cannot be torn by concurrent saves) and no per-row lock churn.
	if _, err := conn.ExecContext(ctx, "BEGIN DEFERRED"); err != nil {
		return nil, fmt.Errorf("begin deferred: %w", err)
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
	}()

	seeds, err := loadDupeRows(ctx, conn, req)
	if err != nil {
		return nil, err
	}

	// Prepared once: skips re-parsing the FTS query for every seed.
	stmt, err := conn.PrepareContext(ctx, `
		SELECT m.id, m.kind, m.task_type, m.title, m.what, m.learned, m.tags, m.created_at
		FROM memories_fts fts
		JOIN memories m ON m.rowid = fts.rowid
		WHERE memories_fts MATCH ?
		AND m.task_type = ?
		AND m.kind = ?
		ORDER BY bm25(memories_fts, 3, 1, 2, 1)
		LIMIT ?
	`)
	if err != nil {
		return nil, fmt.Errorf("prepare candidate query: %w", err)
	}
	defer stmt.Close()

	resp := &SuggestDupesResponse{
		Status:      "ok",
		Threshold:   req.Threshold,
		RowsScanned: len(seeds),
		Clusters:    []DupeCluster{},
	}
	consumed := make(map[string]struct{}, len(seeds))
	tokenCache := make(map[string]map[string]struct{}, len(seeds))
	tokensFor := func(id, text string) map[string]struct{} {
		if ts, ok := tokenCache[id]; ok {
			return ts
		}
		ts := tokenSet(text)
		tokenCache[id] = ts
		return ts
	}

	for _, seed := range seeds {
		if _, done := consumed[seed.id]; done {
			continue
		}
		consumed[seed.id] = struct{}{}

		query := dupeQuery(seed.text)
		if query == "" {
			continue
		}
		seedTokens := tokensFor(seed.id, seed.text)
		if len(seedTokens) == 0 {
			continue
		}

		rows, err := stmt.QueryContext(ctx, query, seed.taskType, seed.kind, bm25CandidateLimit)
		if err != nil {
			return nil, fmt.Errorf("candidate query for %s: %w", seed.id, err)
		}
		members := []DupeMember{{
			ID: seed.id, Title: seed.title, Kind: seed.kind,
			TaskType: seed.taskType, CreatedAt: seed.createdAt, Score: 1,
		}}
		for rows.Next() {
			var c dupeRow
			var what, learned, tags string
			if err := rows.Scan(&c.id, &c.kind, &c.taskType, &c.title, &what, &learned, &tags, &c.createdAt); err != nil {
				_ = rows.Close() //nolint:sqlclosecheck // defer would leak across loop iterations; explicit close on error path
				return nil, fmt.Errorf("scan candidate: %w", err)
			}
			if c.id == seed.id {
				continue
			}
			if _, done := consumed[c.id]; done {
				continue
			}
			c.text = c.title + " " + what + " " + learned + " " + tags
			score := jaccard(seedTokens, tokensFor(c.id, c.text))
			if score >= req.Threshold {
				consumed[c.id] = struct{}{}
				members = append(members, DupeMember{
					ID: c.id, Title: c.title, Kind: c.kind,
					TaskType: c.taskType, CreatedAt: c.createdAt, Score: score,
				})
			}
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate candidates for %s: %w", seed.id, err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("close candidates: %w", err)
		}
		if len(members) > 1 {
			resp.Clusters = append(resp.Clusters, DupeCluster{SeedID: seed.id, Members: members})
		}
	}

	return resp, nil
}

// loadDupeRows returns the scan pool in created_at,id order so greedy
// clustering is deterministic and runs are reproducible.
func loadDupeRows(ctx context.Context, conn *sql.Conn, req SuggestDupesRequest) ([]dupeRow, error) {
	conds := []string{"1=1"}
	args := []any{}
	if req.Kind != "" {
		conds = append(conds, "kind = ?")
		args = append(args, req.Kind)
	}
	if req.TaskType != "" {
		conds = append(conds, "task_type = ?")
		args = append(args, req.TaskType)
	}
	//nolint:gosec // G202: conds are hardcoded column predicates ("kind = ?", "task_type = ?"); args parameterized
	scanSQL := `SELECT id, kind, task_type, title, what, learned, tags, created_at FROM memories WHERE ` +
		strings.Join(conds, " AND ") + ` ORDER BY created_at, id`
	rows, err := conn.QueryContext(ctx, scanSQL, args...)
	if err != nil {
		return nil, fmt.Errorf("load scan pool: %w", err)
	}
	defer rows.Close()
	out := []dupeRow{}
	for rows.Next() {
		var r dupeRow
		var what, learned, tags string
		if err := rows.Scan(&r.id, &r.kind, &r.taskType, &r.title, &what, &learned, &tags, &r.createdAt); err != nil {
			return nil, fmt.Errorf("scan pool row: %w", err)
		}
		r.text = r.title + " " + what + " " + learned + " " + tags
		out = append(out, r)
	}
	return out, rows.Err()
}

// dupeQuery builds the same capped, phrase-quoted OR query the save-time
// near-duplicate check uses (decision #19; operator-keyword quoting per the
// nearDuplicateConn threat model).
func dupeQuery(text string) string {
	terms := searchTerms(text)
	if len(terms) == 0 {
		return ""
	}
	if len(terms) > bm25QueryTermCap {
		sortTermsByIDF(terms)
		terms = terms[:bm25QueryTermCap]
	}
	quoted := make([]string, len(terms))
	for i, t := range terms {
		quoted[i] = `"` + t + `"`
	}
	return strings.Join(quoted, " OR ")
}
