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

func (s *Store) Get(ctx context.Context, id string) (*Memory, error) {
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
