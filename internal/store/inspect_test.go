package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/samuelmolero26/droids-mem/internal/store"
)

// TestGetRow_ExposesLifecycleFields covers the Memory projection (inspect.go)
// added by the v5→v6 lifecycle layer: ReviewAfter/Pinned scanned from the DB,
// NeedsReview computed in Go from ReviewAfter vs now.
func TestGetRow_ExposesLifecycleFields(t *testing.T) {
	s, conn := newTestStoreWithConn(t)
	resp, err := s.Save(context.Background(), validReq())
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	past := time.Now().Add(-time.Hour).Unix()
	if _, err := conn.Exec(`UPDATE memories SET review_after = ?, pinned = 1 WHERE id = ?`, past, resp.ID); err != nil {
		t.Fatalf("seed lifecycle fields: %v", err)
	}

	m, err := s.GetRow(context.Background(), resp.ID)
	if err != nil {
		t.Fatalf("GetRow: %v", err)
	}
	if m.ReviewAfter == nil || *m.ReviewAfter != past {
		t.Errorf("ReviewAfter = %v, want %d", m.ReviewAfter, past)
	}
	if !m.Pinned {
		t.Error("Pinned = false, want true")
	}
	if !m.NeedsReview {
		t.Error("NeedsReview = false, want true (review_after in the past)")
	}
}

// TestGetRow_NullReviewAfterStaysNil guards D1/D4: a row with no review_after
// (the grandfathered/no-decay-yet state — decay-on-save is slice 3) must
// scan to a nil *int64, not a COALESCEd zero, and NeedsReview must be false.
func TestGetRow_NullReviewAfterStaysNil(t *testing.T) {
	s := newTestStore(t)
	resp, err := s.Save(context.Background(), validReq())
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	m, err := s.GetRow(context.Background(), resp.ID)
	if err != nil {
		t.Fatalf("GetRow: %v", err)
	}
	if m.ReviewAfter != nil {
		t.Errorf("ReviewAfter = %v, want nil", m.ReviewAfter)
	}
	if m.NeedsReview {
		t.Error("NeedsReview = true, want false")
	}
	if m.Pinned {
		t.Error("Pinned = true, want false")
	}
}

func TestList_ExposesLifecycleFields(t *testing.T) {
	s, conn := newTestStoreWithConn(t)
	resp, err := s.Save(context.Background(), validReq())
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := conn.Exec(`UPDATE memories SET pinned = 1 WHERE id = ?`, resp.ID); err != nil {
		t.Fatalf("seed pinned: %v", err)
	}

	listResp, err := s.List(context.Background(), store.ListRequest{TaskType: "crm_upload"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listResp.Memories) != 1 {
		t.Fatalf("expected 1 memory, got %d", len(listResp.Memories))
	}
	if !listResp.Memories[0].Pinned {
		t.Error("Pinned = false, want true")
	}
}
