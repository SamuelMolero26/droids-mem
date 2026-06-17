package store

import (
	"context"
	"log"
	"time"
)

// recordExpansion bumps the Expand signal (ADR-0013) for the Memory id: it
// increments expand_count and stamps last_expanded_at with the current unix
// time in SECONDS (matching created_at/updated_at — never milliseconds).
//
// Best-effort telemetry: every failure is swallowed (logged, never returned) so
// a counter that cannot write does not fail the Get it measures. The write can
// fail benignly under write-lock contention, request-context cancellation, or a
// concurrent prune of the row (0 rows affected) — all acceptable, since the
// signal is approximate and total-lifetime, not exact.
//
// It is a single autocommit UPDATE on the request context with no wrapping
// transaction, and touches none of title/what/learned/tags, so the scoped
// memories_au trigger does not re-index FTS on the read hot path.
func (s *Store) recordExpansion(ctx context.Context, id string) {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE memories
		SET expand_count = expand_count + 1, last_expanded_at = ?
		WHERE id = ?
	`, time.Now().Unix(), id); err != nil {
		log.Printf("expand signal: increment failed for %s: %v", id, err)
	}
}
