package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type Memory struct {
	ID          string `json:"id"`
	SessionID   string `json:"session_id"`
	TaskType    string `json:"task_type"`
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	What        string `json:"what"`
	Learned     string `json:"learned"`
	Tags        string `json:"tags"`
	Fingerprint string `json:"fingerprint"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

type ListRequest struct {
	TaskType string
	Kind     string
	Limit    int
}

type ListResponse struct {
	Memories []Memory `json:"memories"`
	Total    int      `json:"total"`
}

func (s *Store) List(ctx context.Context, req ListRequest) (*ListResponse, error) {
	if req.Kind != "" && !validKinds[req.Kind] {
		return nil, &ValidationError{Field: "kind", Message: "must be one of: error_resolution, task_pattern, user_rule, session_summary"}
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	var conditions []string
	var args []any

	if req.TaskType != "" {
		conditions = append(conditions, "task_type = ?")
		args = append(args, req.TaskType)
	}
	if req.Kind != "" {
		conditions = append(conditions, "kind = ?")
		args = append(args, req.Kind)
	}

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}
	args = append(args, limit)

	stmt := fmt.Sprintf(`
		SELECT id, session_id, task_type, kind, title, what, learned, tags, fingerprint, created_at, updated_at
		FROM memories %s
		ORDER BY created_at DESC
		LIMIT ?
	`, where)

	rows, err := s.db.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, fmt.Errorf("list query: %w", err)
	}
	defer rows.Close()

	memories := []Memory{}
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.ID, &m.SessionID, &m.TaskType, &m.Kind, &m.Title, &m.What, &m.Learned, &m.Tags, &m.Fingerprint, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan memory: %w", err)
		}
		memories = append(memories, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list rows: %w", err)
	}

	return &ListResponse{Memories: memories, Total: len(memories)}, nil
}

type RecentSessionsRequest struct {
	Limit int
}

type RecentSessionsResponse struct {
	Sessions []Memory `json:"sessions"`
	Total    int      `json:"total"`
}

// RecentSessions returns the newest auto-session-summaries (origin='auto'),
// recency-ordered, regardless of task_type (ADR-0016 pt 7). This is the
// human/operator "what did I do lately across Claude Code runs" view — keyed on
// origin, the cross-cutting axis, served by idx_memories_origin_created, never
// touching FTS. It is deliberately NOT exposed over MCP; the agent reaches auto
// summaries through the relevance-gated mem_search/mem_get path instead.
func (s *Store) RecentSessions(ctx context.Context, req RecentSessionsRequest) (*RecentSessionsResponse, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, task_type, kind, title, what, learned, tags, fingerprint, created_at, updated_at
		FROM memories
		WHERE origin = 'auto'
		ORDER BY created_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("recent sessions query: %w", err)
	}
	defer rows.Close()

	sessions := []Memory{}
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.ID, &m.SessionID, &m.TaskType, &m.Kind, &m.Title, &m.What, &m.Learned, &m.Tags, &m.Fingerprint, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		sessions = append(sessions, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("recent sessions rows: %w", err)
	}

	return &RecentSessionsResponse{Sessions: sessions, Total: len(sessions)}, nil
}

// GetRow is the pure, non-counting fetch of a single Memory by id. It records
// NO Expand signal — use it for operator/TUI reads (the Memory inspector) and
// anywhere an internal lookup must not be mistaken for agent consumption.
// Agent-facing callers (CLI `get`, MCP `mem_get`) want Get instead.
func (s *Store) GetRow(ctx context.Context, id string) (*Memory, error) {
	if strings.TrimSpace(id) == "" {
		return nil, &ValidationError{Field: "id", Message: "required"}
	}

	var m Memory
	err := s.db.QueryRowContext(ctx, `
		SELECT id, session_id, task_type, kind, title, what, learned, tags, fingerprint, created_at, updated_at
		FROM memories WHERE id = ?
	`, id).Scan(&m.ID, &m.SessionID, &m.TaskType, &m.Kind, &m.Title, &m.What, &m.Learned, &m.Tags, &m.Fingerprint, &m.CreatedAt, &m.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get memory: %w", err)
	}
	return &m, nil
}

// CountsResponse is the static corpus census the Memory inspector sidebar shows:
// total rows per Kind plus the grand total. Read-only and non-counting (ADR-0021).
type CountsResponse struct {
	ByKind map[string]int `json:"by_kind"`
	Total  int            `json:"total"`
}

// Counts returns the whole-corpus census (rows per Kind + total) in one grouped
// query. The inspector computes it once on load and refreshes only after a
// delete — it is deliberately not recomputed per keystroke.
func (s *Store) Counts(ctx context.Context) (*CountsResponse, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT kind, COUNT(*) FROM memories GROUP BY kind
	`)
	if err != nil {
		return nil, fmt.Errorf("counts query: %w", err)
	}
	defer rows.Close()

	resp := &CountsResponse{ByKind: map[string]int{}}
	for rows.Next() {
		var kind string
		var n int
		if err := rows.Scan(&kind, &n); err != nil {
			return nil, fmt.Errorf("scan count: %w", err)
		}
		resp.ByKind[kind] = n
		resp.Total += n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("counts rows: %w", err)
	}
	return resp, nil
}

// Get is the agent-facing fetch: GetRow plus a recorded Expand signal (ADR-0013).
// It is the path behind CLI `get` and MCP `mem_get`. The increment is
// best-effort telemetry — a hit returns the Memory whether or not the signal
// could be written (see recordExpansion). Operator/TUI reads must use GetRow so
// browsing does not pollute the signal.
func (s *Store) Get(ctx context.Context, id string) (*Memory, error) {
	m, err := s.GetRow(ctx, id)
	if err != nil || m == nil {
		return m, err
	}
	s.recordExpansion(ctx, m.ID)
	return m, nil
}
