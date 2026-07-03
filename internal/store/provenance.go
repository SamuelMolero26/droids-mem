package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// File-provenance relation (ADR-0021 Phase 2): the files a session read or
// changed, keyed by droids-mem session_id. Orthogonal to the Memory model — no
// scrub, no dedupe, no FTS. Written at session-summary flush from the paths the
// PostToolUse hook captured; read by the future Graph tab's file→node join.

// RecordFiles attaches file paths to a session_id (INSERT OR IGNORE, so the
// composite PK collapses repeat touches and re-flushes are idempotent). Empty
// paths are skipped. A blank session_id is a no-op — provenance without a
// session to hang it on is meaningless.
func (s *Store) RecordFiles(ctx context.Context, sessionID string, paths []string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || len(paths) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin record files: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	now := time.Now().Unix()
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO memory_files(session_id, file_path, created_at) VALUES (?, ?, ?)`,
			sessionID, p, now,
		); err != nil {
			return fmt.Errorf("insert file provenance: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit record files: %w", err)
	}
	return nil
}

// FilesForSession returns the file paths recorded for a session_id, oldest
// first. Used by the file→graph-node join and operator inspection.
func (s *Store) FilesForSession(ctx context.Context, sessionID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT file_path FROM memory_files WHERE session_id = ? ORDER BY created_at, file_path`,
		strings.TrimSpace(sessionID),
	)
	if err != nil {
		return nil, fmt.Errorf("files for session: %w", err)
	}
	defer rows.Close()

	paths := []string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("scan file path: %w", err)
		}
		paths = append(paths, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("files rows: %w", err)
	}
	return paths, nil
}
