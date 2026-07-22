package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// MarkReviewed advances a memory's review_after to now+horizon[kind] as a
// metadata-only UPDATE — no wrapping transaction, and it never touches
// title/what/learned/tags, so memories_au (AFTER UPDATE OF those columns)
// does not refire and updated_at is left unchanged. Mirrors recordExpansion
// (expand.go).
//
// Returns (nil, nil) for an unknown id — the CLI maps that to exit 3
// (not found). Returns a *ValidationError for an exempt kind (no decay
// horizon, e.g. session_summary) — the CLI maps that to exit 2 (usage):
// there is nothing to reset.
func (s *Store) MarkReviewed(ctx context.Context, id string) (*Memory, error) {
	if strings.TrimSpace(id) == "" {
		return nil, &ValidationError{Field: "id", Message: "required"}
	}
	m, err := s.GetRow(ctx, id)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, nil
	}
	horizon, ok := decayHorizon[m.Kind]
	if !ok {
		return nil, &ValidationError{
			Code:       "exempt_kind",
			Field:      "kind",
			Message:    fmt.Sprintf("%s has no decay horizon and cannot be marked reviewed", m.Kind),
			Retryable:  false,
			Suggestion: "mark_reviewed only applies to error_resolution, task_pattern, or user_rule",
		}
	}

	newReviewAfter := time.Now().Add(horizon).Unix()
	if _, err := s.db.ExecContext(ctx, `
		UPDATE memories SET review_after = ? WHERE id = ?
	`, newReviewAfter, id); err != nil {
		return nil, fmt.Errorf("mark reviewed: %w", err)
	}
	return s.GetRow(ctx, id)
}

// ReviewListResponse is the `review list` payload: every memory whose
// review_after has passed (the needs_review set), most-overdue first.
// archived_memories is a separate table never joined here, so archived rows
// are excluded by construction (spec: "archival, not review status, governs
// visibility").
type ReviewListResponse struct {
	Memories []Memory `json:"memories"`
	Total    int      `json:"total"`
}

// ReviewList returns the needs_review set: review_after IS NOT NULL AND
// review_after < now. This is the one read path that DOES filter on
// review_after — it is the operator's "what needs a second look" view, not
// an agent-facing retrieval path, so it does not conflict with D4's
// audit-only rule for mem_context/mem_search.
func (s *Store) ReviewList(ctx context.Context) (*ReviewListResponse, error) {
	now := time.Now().Unix()
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, task_type, kind, title, what, learned, tags, fingerprint, created_at, updated_at,
		       expand_count, COALESCE(last_expanded_at, 0), scope, review_after, pinned
		FROM memories
		WHERE review_after IS NOT NULL AND review_after < ?
		ORDER BY review_after ASC
	`, now)
	if err != nil {
		return nil, fmt.Errorf("review list query: %w", err)
	}
	defer rows.Close()

	memories := []Memory{}
	for rows.Next() {
		var m Memory
		var reviewAfter sql.NullInt64
		if err := rows.Scan(&m.ID, &m.SessionID, &m.TaskType, &m.Kind, &m.Title, &m.What, &m.Learned, &m.Tags, &m.Fingerprint, &m.CreatedAt, &m.UpdatedAt, &m.ExpandCount, &m.LastExpandedAt, &m.Scope, &reviewAfter, &m.Pinned); err != nil {
			return nil, fmt.Errorf("scan review row: %w", err)
		}
		if reviewAfter.Valid {
			m.ReviewAfter = &reviewAfter.Int64
		}
		m.NeedsReview = needsReview(m.ReviewAfter)
		memories = append(memories, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("review list rows: %w", err)
	}
	return &ReviewListResponse{Memories: memories, Total: len(memories)}, nil
}

// ArchivedMemory is a retired row from the Memory archive: the full memory plus
// the unix second it was archived (ADR-0030). Recoverable/auditable but invisible
// to every retrieval path.
type ArchivedMemory struct {
	Memory
	ArchivedAt int64 `json:"archived_at"`
}

// ArchiveListResponse is the `archive list` payload, most-recently-archived first.
type ArchiveListResponse struct {
	Memories []ArchivedMemory `json:"memories"`
	Total    int              `json:"total"`
}

// ArchiveList returns archived (superseded) memories, optionally filtered by
// task_type — the operator audit view over the soft-delete archive. Never joined
// to memories_fts and never returned by any agent-facing read, so archived rows
// stay out of mem_context/mem_search/review list.
func (s *Store) ArchiveList(ctx context.Context, taskType string) (*ArchiveListResponse, error) {
	query := `
		SELECT id, session_id, task_type, kind, title, what, learned, tags, fingerprint, created_at, updated_at,
		       expand_count, COALESCE(last_expanded_at, 0), scope, review_after, pinned, archived_at
		FROM archived_memories
	`
	var args []any
	if tt := strings.TrimSpace(taskType); tt != "" {
		query += " WHERE task_type = ?"
		args = append(args, tt)
	}
	query += " ORDER BY archived_at DESC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("archive list query: %w", err)
	}
	defer rows.Close()

	memories := []ArchivedMemory{}
	for rows.Next() {
		var am ArchivedMemory
		var reviewAfter sql.NullInt64
		if err := rows.Scan(&am.ID, &am.SessionID, &am.TaskType, &am.Kind, &am.Title, &am.What, &am.Learned, &am.Tags, &am.Fingerprint, &am.CreatedAt, &am.UpdatedAt, &am.ExpandCount, &am.LastExpandedAt, &am.Scope, &reviewAfter, &am.Pinned, &am.ArchivedAt); err != nil {
			return nil, fmt.Errorf("scan archived row: %w", err)
		}
		if reviewAfter.Valid {
			am.ReviewAfter = &reviewAfter.Int64
		}
		am.NeedsReview = needsReview(am.ReviewAfter)
		memories = append(memories, am)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("archive list rows: %w", err)
	}
	return &ArchiveListResponse{Memories: memories, Total: len(memories)}, nil
}

// maxPinnedPerTaskType caps how many memories a single task_type may pin
// (spec: "Pin Assignment and Per-Task-Type Cap"). Enforced race-free by the
// single-writer connection pool (db.go SetMaxOpenConns(1)).
const maxPinnedPerTaskType = 5

// Pin sets pinned=1 for id, scoped to that memory's own task_type, capped at
// maxPinnedPerTaskType pinned rows per task_type. Idempotent: pinning an
// already-pinned id is a no-op that does not consume a cap slot. Metadata-only
// UPDATE — same no-FTS-refire, no-updated_at-bump contract as MarkReviewed.
func (s *Store) Pin(ctx context.Context, id string) (*Memory, error) {
	if strings.TrimSpace(id) == "" {
		return nil, &ValidationError{Field: "id", Message: "required"}
	}
	m, err := s.GetRow(ctx, id)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, nil
	}
	if m.Pinned {
		return m, nil
	}

	var count int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM memories WHERE pinned = 1 AND task_type = ?
	`, m.TaskType).Scan(&count); err != nil {
		return nil, fmt.Errorf("count pinned: %w", err)
	}
	if count >= maxPinnedPerTaskType {
		return nil, &ValidationError{
			Code:       "pin_cap_exceeded",
			Field:      "id",
			Message:    fmt.Sprintf("task_type %q already has %d pinned memories (cap %d)", m.TaskType, count, maxPinnedPerTaskType),
			Retryable:  false,
			Suggestion: "unpin an existing memory for this task_type before pinning another",
		}
	}

	if _, err := s.db.ExecContext(ctx, `UPDATE memories SET pinned = 1 WHERE id = ?`, id); err != nil {
		return nil, fmt.Errorf("pin: %w", err)
	}
	return s.GetRow(ctx, id)
}

// Unpin sets pinned=0 for id. Idempotent: unpinning an already-unpinned id is
// a no-op.
func (s *Store) Unpin(ctx context.Context, id string) (*Memory, error) {
	if strings.TrimSpace(id) == "" {
		return nil, &ValidationError{Field: "id", Message: "required"}
	}
	m, err := s.GetRow(ctx, id)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, nil
	}
	if !m.Pinned {
		return m, nil
	}

	if _, err := s.db.ExecContext(ctx, `UPDATE memories SET pinned = 0 WHERE id = ?`, id); err != nil {
		return nil, fmt.Errorf("unpin: %w", err)
	}
	return s.GetRow(ctx, id)
}
