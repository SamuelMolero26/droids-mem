package store_test

import (
	"context"
	"testing"
	"time"
)

// TestSave_SetsDecayHorizon is the RED for Phase 3 (ADR-0031): every save
// must stamp review_after = created_at + horizon[kind], per the per-kind
// decay horizon (error_resolution/task_pattern=90d, user_rule=365d).
func TestSave_SetsDecayHorizon(t *testing.T) {
	tests := []struct {
		kind    string
		horizon time.Duration
	}{
		{"error_resolution", 90 * 24 * time.Hour},
		{"task_pattern", 90 * 24 * time.Hour},
		{"user_rule", 365 * 24 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			s := newTestStore(t)
			req := validReq()
			req.Kind = tt.kind
			resp, err := s.Save(context.Background(), req)
			if err != nil {
				t.Fatalf("Save: %v", err)
			}
			m, err := s.GetRow(context.Background(), resp.ID)
			if err != nil {
				t.Fatalf("GetRow: %v", err)
			}
			if m.ReviewAfter == nil {
				t.Fatal("ReviewAfter = nil, want created_at+horizon")
			}
			want := m.CreatedAt + int64(tt.horizon.Seconds())
			if *m.ReviewAfter != want {
				t.Errorf("ReviewAfter = %d, want %d (created_at %d + horizon %s)", *m.ReviewAfter, want, m.CreatedAt, tt.horizon)
			}
			if m.NeedsReview {
				t.Error("NeedsReview = true immediately after save, want false (horizon is in the future)")
			}
		})
	}
}

// TestSave_SessionSummaryReviewAfterExempt guards the spec's "session_summary
// save is exempt" scenario: review_after stays NULL forever for this kind, so
// needs_review can never fire for it.
func TestSave_SessionSummaryReviewAfterExempt(t *testing.T) {
	s := newTestStore(t)
	req := validReq()
	req.Kind = "session_summary"
	resp, err := s.Save(context.Background(), req)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	m, err := s.GetRow(context.Background(), resp.ID)
	if err != nil {
		t.Fatalf("GetRow: %v", err)
	}
	if m.ReviewAfter != nil {
		t.Errorf("ReviewAfter = %v, want nil (session_summary is decay-exempt)", *m.ReviewAfter)
	}
	if m.NeedsReview {
		t.Error("NeedsReview = true, want false")
	}
}

// TestForceSave_RefreshesReviewAfter is the RED for "content write resets the
// clock": a force-save overwrite must recompute review_after from the new
// now, never preserve a stale value. A direct SQL seed simulates a review_after
// that is already past-due (as if 90 days had elapsed) without a real sleep.
func TestForceSave_RefreshesReviewAfter(t *testing.T) {
	s, conn := newTestStoreWithConn(t)
	req := validReq() // error_resolution, 90d horizon
	resp, err := s.Save(context.Background(), req)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	stale := time.Now().Add(-time.Hour).Unix()
	if _, err := conn.Exec(`UPDATE memories SET review_after = ? WHERE id = ?`, stale, resp.ID); err != nil {
		t.Fatalf("seed stale review_after: %v", err)
	}

	req.Force = true
	req.What = req.What + " — updated context for force overwrite"
	if _, err := s.Save(context.Background(), req); err != nil {
		t.Fatalf("force Save: %v", err)
	}

	m, err := s.GetRow(context.Background(), resp.ID)
	if err != nil {
		t.Fatalf("GetRow: %v", err)
	}
	if m.ReviewAfter == nil {
		t.Fatal("ReviewAfter = nil after force overwrite, want a fresh horizon")
	}
	if *m.ReviewAfter == stale {
		t.Error("ReviewAfter unchanged after force overwrite — clock was not refreshed")
	}
	wantFloor := time.Now().Add(89*24*time.Hour + 23*time.Hour).Unix()
	if *m.ReviewAfter < wantFloor {
		t.Errorf("ReviewAfter = %d, want ~now+90d (floor %d)", *m.ReviewAfter, wantFloor)
	}
	if m.NeedsReview {
		t.Error("NeedsReview = true after fresh review_after, want false")
	}
}
