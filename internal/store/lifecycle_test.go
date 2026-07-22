package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/samuelmolero26/droids-mem/internal/store"
)

// TestMarkReviewed_AdvancesClockWithoutSideEffects is the [GUARD] for Phase 4:
// mark_reviewed must be a pure metadata write — review_after advances to
// now+horizon[kind], updated_at is untouched, and the memories_fts row is
// untouched (memories_au only fires on UPDATE OF title/what/learned/tags).
func TestMarkReviewed_AdvancesClockWithoutSideEffects(t *testing.T) {
	s, conn := newTestStoreWithConn(t)
	resp, err := s.Save(context.Background(), validReq()) // error_resolution, 90d horizon
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Seed a stale (already-overdue) review_after directly, simulating a row
	// that is genuinely due for review — this is the realistic precondition
	// for calling mark_reviewed, and makes the "advances the clock" assertion
	// below independent of same-second test timing races against the row's
	// freshly-saved horizon.
	stale := time.Now().Add(-time.Hour).Unix()
	if _, err := conn.Exec(`UPDATE memories SET review_after = ? WHERE id = ?`, stale, resp.ID); err != nil {
		t.Fatalf("seed stale review_after: %v", err)
	}

	before, err := s.GetRow(context.Background(), resp.ID)
	if err != nil {
		t.Fatalf("GetRow before: %v", err)
	}
	if !before.NeedsReview {
		t.Fatal("precondition failed: seeded row should be needs_review before mark_reviewed")
	}

	var ftsBefore string
	if err := conn.QueryRow(`SELECT title || what || learned || tags FROM memories_fts WHERE rowid = (SELECT rowid FROM memories WHERE id = ?)`, resp.ID).Scan(&ftsBefore); err != nil {
		t.Fatalf("read fts before: %v", err)
	}

	m, err := s.MarkReviewed(context.Background(), resp.ID)
	if err != nil {
		t.Fatalf("MarkReviewed: %v", err)
	}
	if m == nil {
		t.Fatal("MarkReviewed returned nil memory for a valid id")
	}

	if m.ReviewAfter == nil {
		t.Fatal("ReviewAfter = nil after mark_reviewed, want now+horizon")
	}
	if before.ReviewAfter != nil && *m.ReviewAfter <= *before.ReviewAfter {
		t.Errorf("ReviewAfter did not advance: before=%d after=%d", *before.ReviewAfter, *m.ReviewAfter)
	}
	wantFloor := time.Now().Add(89*24*time.Hour + 23*time.Hour).Unix()
	if *m.ReviewAfter < wantFloor {
		t.Errorf("ReviewAfter = %d, want ~now+90d (floor %d)", *m.ReviewAfter, wantFloor)
	}
	if m.NeedsReview {
		t.Error("NeedsReview = true right after mark_reviewed, want false")
	}
	if m.UpdatedAt != before.UpdatedAt {
		t.Errorf("UpdatedAt changed: before=%d after=%d, want unchanged (metadata-only write)", before.UpdatedAt, m.UpdatedAt)
	}

	var ftsAfter string
	if err := conn.QueryRow(`SELECT title || what || learned || tags FROM memories_fts WHERE rowid = (SELECT rowid FROM memories WHERE id = ?)`, resp.ID).Scan(&ftsAfter); err != nil {
		t.Fatalf("read fts after: %v", err)
	}
	if ftsBefore != ftsAfter {
		t.Errorf("memories_fts row changed after mark_reviewed — trigger refired, want unchanged")
	}
}

func TestMarkReviewed_UnknownID(t *testing.T) {
	s := newTestStore(t)
	m, err := s.MarkReviewed(context.Background(), "mem_does_not_exist")
	if err != nil {
		t.Fatalf("MarkReviewed unexpected error: %v", err)
	}
	if m != nil {
		t.Error("expected nil memory for unknown id (caller maps to not-found)")
	}
}

func TestMarkReviewed_ExemptKindRejected(t *testing.T) {
	s := newTestStore(t)
	req := validReq()
	req.Kind = "session_summary"
	resp, err := s.Save(context.Background(), req)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	_, err = s.MarkReviewed(context.Background(), resp.ID)
	if err == nil {
		t.Fatal("expected ValidationError for exempt kind, got nil")
	}
	var ve *store.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *store.ValidationError, got %T: %v", err, err)
	}
}

// TestReviewList_ReturnsNeedsReviewSet covers the `review list` read: only
// rows with a past-due review_after appear, ordered by most-overdue first
// (ascending review_after), and future/exempt rows are excluded.
func TestReviewList_ReturnsNeedsReviewSet(t *testing.T) {
	s, conn := newTestStoreWithConn(t)

	dueSoon, err := s.Save(context.Background(), validReq())
	if err != nil {
		t.Fatalf("Save due-soon: %v", err)
	}
	req2 := validReq()
	req2.Title = "Second lesson"
	req2.Learned = "second learned lesson body"
	overdue, err := s.Save(context.Background(), req2)
	if err != nil {
		t.Fatalf("Save overdue: %v", err)
	}
	req3 := validReq()
	req3.Kind = "session_summary"
	req3.Title = "Exempt summary"
	req3.Learned = "exempt lesson body"
	exempt, err := s.Save(context.Background(), req3)
	if err != nil {
		t.Fatalf("Save exempt: %v", err)
	}

	past := time.Now().Add(-time.Hour).Unix()
	if _, err := conn.Exec(`UPDATE memories SET review_after = ? WHERE id = ?`, past, overdue.ID); err != nil {
		t.Fatalf("seed overdue review_after: %v", err)
	}

	resp, err := s.ReviewList(context.Background())
	if err != nil {
		t.Fatalf("ReviewList: %v", err)
	}

	ids := map[string]bool{}
	for _, m := range resp.Memories {
		ids[m.ID] = true
	}
	if !ids[overdue.ID] {
		t.Error("expected overdue memory in review list")
	}
	if ids[dueSoon.ID] {
		t.Error("did not expect a not-yet-due memory in review list")
	}
	if ids[exempt.ID] {
		t.Error("did not expect an exempt (session_summary) memory in review list")
	}
}

// TestPin_SucceedsUnderCap covers pin succeeding while a task_type has fewer
// than maxPinnedPerTaskType (5) pinned rows.
func TestPin_SucceedsUnderCap(t *testing.T) {
	s := newTestStore(t)
	resp, err := s.Save(context.Background(), validReq())
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	m, err := s.Pin(context.Background(), resp.ID)
	if err != nil {
		t.Fatalf("Pin: %v", err)
	}
	if m == nil || !m.Pinned {
		t.Fatal("expected pinned memory back")
	}
}

// TestPin_SixthRejected is the [GUARD]: exactly 5 pinned memories for a
// task_type must reject a 6th distinct pin with a ValidationError (CLI maps
// to exit 5), and all 5 prior rows must remain pinned unchanged.
func TestPin_SixthRejected(t *testing.T) {
	s := newTestStore(t)
	taskType := "lifecycle_pin_cap"
	// Distinct, dissimilar topics per seed — near-identical titles/bodies would
	// trip the near-duplicate gate (Jaccard >= 0.85) and skip the save, leaving
	// an empty id. Each seed must survive dedupe to actually get pinned.
	topics := []string{
		"authentication token refresh handling",
		"postgres connection pool exhaustion",
		"websocket reconnect backoff strategy",
		"terminal ansi color rendering quirks",
		"filesystem watcher debounce timing",
	}
	var ids []string
	for i := 0; i < len(topics); i++ {
		req := validReq()
		req.TaskType = taskType
		req.Title = fmt.Sprintf("Lesson on %s", topics[i])
		req.Learned = fmt.Sprintf("detailed unique guidance covering %s in depth", topics[i])
		resp, err := s.Save(context.Background(), req)
		if err != nil {
			t.Fatalf("seed save %d: %v", i, err)
		}
		if _, err := s.Pin(context.Background(), resp.ID); err != nil {
			t.Fatalf("pin %d: %v", i, err)
		}
		ids = append(ids, resp.ID)
	}

	sixthReq := validReq()
	sixthReq.TaskType = taskType
	sixthReq.Title = "Sixth pin candidate"
	sixthReq.Learned = "sixth lesson body"
	sixthResp, err := s.Save(context.Background(), sixthReq)
	if err != nil {
		t.Fatalf("seed sixth save: %v", err)
	}

	_, err = s.Pin(context.Background(), sixthResp.ID)
	if err == nil {
		t.Fatal("expected pin cap ValidationError for 6th distinct pin, got nil")
	}
	var ve *store.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *store.ValidationError, got %T: %v", err, err)
	}

	for _, id := range ids {
		m, err := s.GetRow(context.Background(), id)
		if err != nil {
			t.Fatalf("GetRow %s: %v", id, err)
		}
		if !m.Pinned {
			t.Errorf("pinned row %s lost its pin after rejected 6th pin", id)
		}
	}
	sixth, err := s.GetRow(context.Background(), sixthResp.ID)
	if err != nil {
		t.Fatalf("GetRow sixth: %v", err)
	}
	if sixth.Pinned {
		t.Error("rejected 6th pin should not be pinned")
	}
}

// TestUnpin_FreesSlotAndIsIdempotent covers unpin freeing a cap slot, and
// re-pinning an already-pinned id no-oping without consuming an extra slot.
func TestUnpin_FreesSlotAndIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	resp, err := s.Save(context.Background(), validReq())
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := s.Pin(context.Background(), resp.ID); err != nil {
		t.Fatalf("Pin: %v", err)
	}

	// idempotent re-pin: no error, no-op
	m, err := s.Pin(context.Background(), resp.ID)
	if err != nil {
		t.Fatalf("re-Pin: %v", err)
	}
	if !m.Pinned {
		t.Error("re-pin should stay pinned")
	}

	m, err = s.Unpin(context.Background(), resp.ID)
	if err != nil {
		t.Fatalf("Unpin: %v", err)
	}
	if m.Pinned {
		t.Error("expected unpinned after Unpin")
	}

	// unpin again is idempotent too
	m, err = s.Unpin(context.Background(), resp.ID)
	if err != nil {
		t.Fatalf("re-Unpin: %v", err)
	}
	if m.Pinned {
		t.Error("expected still unpinned after idempotent re-unpin")
	}
}

func TestPin_UnknownID(t *testing.T) {
	s := newTestStore(t)
	m, err := s.Pin(context.Background(), "mem_does_not_exist")
	if err != nil {
		t.Fatalf("Pin unexpected error: %v", err)
	}
	if m != nil {
		t.Error("expected nil memory for unknown id")
	}
}

func TestUnpin_UnknownID(t *testing.T) {
	s := newTestStore(t)
	m, err := s.Unpin(context.Background(), "mem_does_not_exist")
	if err != nil {
		t.Fatalf("Unpin unexpected error: %v", err)
	}
	if m != nil {
		t.Error("expected nil memory for unknown id")
	}
}

func saveWith(t *testing.T, s *store.Store, taskType, title, learned string) string {
	t.Helper()
	req := validReq()
	req.TaskType = taskType
	req.Title = title
	req.Learned = learned
	resp, err := s.Save(context.Background(), req)
	if err != nil {
		t.Fatalf("save %q: %v", title, err)
	}
	if resp.ID == "" {
		t.Fatalf("save %q returned empty id (deduped?)", title)
	}
	return resp.ID
}

// TestContext_PinnedSection is the [GUARD] for Phase 7: the pinned[] section
// carries this task_type's pinned rows (full body, tier=always), and a pin in a
// different task_type never leaks in.
func TestContext_PinnedSection(t *testing.T) {
	s := newTestStore(t)
	a1 := saveWith(t, s, "pinsec_a", "Redis eviction policy tuning", "guidance on redis maxmemory-policy for lru workloads")
	a2 := saveWith(t, s, "pinsec_a", "Nginx upstream keepalive config", "guidance on nginx upstream keepalive connection reuse")
	b1 := saveWith(t, s, "pinsec_b", "Kafka consumer lag alerting", "guidance on kafka consumer group lag thresholds")

	for _, id := range []string{a1, a2, b1} {
		if _, err := s.Pin(context.Background(), id); err != nil {
			t.Fatalf("pin %s: %v", id, err)
		}
	}

	resp, err := s.Context(context.Background(), store.ContextRequest{TaskType: "pinsec_a"})
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	if len(resp.Pinned) != 2 {
		t.Fatalf("expected 2 pinned in section, got %d", len(resp.Pinned))
	}
	got := map[string]bool{}
	for _, m := range resp.Pinned {
		got[m.ID] = true
		if m.Tier != "always" {
			t.Errorf("pinned %s tier=%q, want always", m.ID, m.Tier)
		}
		if m.Learned == "" {
			t.Errorf("pinned %s has empty Learned, want full body", m.ID)
		}
		if !m.Pinned {
			t.Errorf("pinned %s Pinned=false", m.ID)
		}
	}
	if !got[a1] || !got[a2] {
		t.Error("expected both task_type-A pins in the pinned section")
	}
	if got[b1] {
		t.Error("task_type B pin leaked into task_type A bundle")
	}
}

// TestContext_PinnedOverdueStillShown proves the audit-only rule holds for pins:
// an overdue (needs_review) pinned row is STILL surfaced, just flagged — decay
// never removes it.
func TestContext_PinnedOverdueStillShown(t *testing.T) {
	s, conn := newTestStoreWithConn(t)
	id := saveWith(t, s, "pinsec_c", "TLS cert rotation runbook", "guidance on rotating tls certs before expiry window")
	if _, err := s.Pin(context.Background(), id); err != nil {
		t.Fatalf("pin: %v", err)
	}
	past := time.Now().Add(-time.Hour).Unix()
	if _, err := conn.Exec(`UPDATE memories SET review_after = ? WHERE id = ?`, past, id); err != nil {
		t.Fatalf("seed overdue: %v", err)
	}
	resp, err := s.Context(context.Background(), store.ContextRequest{TaskType: "pinsec_c"})
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	if len(resp.Pinned) != 1 {
		t.Fatalf("expected overdue pinned still shown, got %d", len(resp.Pinned))
	}
	if !resp.Pinned[0].NeedsReview {
		t.Error("overdue pinned should carry needs_review=true (audit-only)")
	}
}
