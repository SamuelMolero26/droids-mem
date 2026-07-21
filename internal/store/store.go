package store

import (
	"context"
	"database/sql"
	"fmt"
)

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// DB returns the underlying *sql.DB. Exposed for tests and operator tooling
// (doctor, inspect) that need to issue ad-hoc queries outside the Store API.
// Production save/search/context paths must go through Store methods so the
// dedupe + transaction discipline stays in one place.
func (s *Store) DB() *sql.DB {
	return s.db
}

// TaskTypeCount is a single row in the corpus census: the task_type slug,
// how many memories it has, and the title of its most recent session_summary.
type TaskTypeCount struct {
	TaskType      string `json:"task_type"`
	Count         int    `json:"count"`
	LatestSession string `json:"latest_session,omitempty"`
}

// ListTaskTypes returns every task_type in the corpus, its memory count, and
// the title of its newest session_summary (empty string if none exist).
func (s *Store) ListTaskTypes(ctx context.Context) ([]TaskTypeCount, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.task_type, COUNT(*),
		       COALESCE((SELECT title FROM memories
		                  WHERE task_type = m.task_type AND kind = 'session_summary'
		                  ORDER BY created_at DESC LIMIT 1), '')
		FROM memories m
		GROUP BY m.task_type
		ORDER BY m.task_type
	`)
	if err != nil {
		return nil, fmt.Errorf("list task types: %w", err)
	}
	defer rows.Close()

	var out []TaskTypeCount
	for rows.Next() {
		var ttc TaskTypeCount
		if err := rows.Scan(&ttc.TaskType, &ttc.Count, &ttc.LatestSession); err != nil {
			return nil, fmt.Errorf("scan task type: %w", err)
		}
		out = append(out, ttc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list task types rows: %w", err)
	}
	return out, nil
}
