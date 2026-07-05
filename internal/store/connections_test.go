package store_test

import (
	"context"
	"testing"

	"github.com/samuelmolero26/droids-mem/internal/store"
)

// TestNeighbors_RanksRelatedCrossKind: Neighbors surfaces the most token-similar
// memories to a seed, spanning kinds/task_types, excludes the seed itself, and
// drops zero-overlap rows.
func TestNeighbors_RanksRelatedCrossKind(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	seed, err := s.Save(ctx, store.SaveRequest{
		TaskType: "crm_upload", Kind: "error_resolution",
		Title:   "HubSpot phone field mapping failure",
		What:    "upload failed because the phone field was rejected",
		Learned: "map the phone number column to the phone property",
	})
	if err != nil {
		t.Fatalf("save seed: %v", err)
	}
	// Related: shares phone/field/mapping tokens but a different kind + task_type.
	related, err := s.Save(ctx, store.SaveRequest{
		TaskType: "hubspot_sync", Kind: "task_pattern",
		Title:   "phone field mapping pattern",
		What:    "when a phone field is rejected re-map the column",
		Learned: "always map phone number to phone before upload",
	})
	if err != nil {
		t.Fatalf("save related: %v", err)
	}
	// Unrelated: no overlapping tokens.
	if _, err := s.Save(ctx, store.SaveRequest{
		TaskType: "billing", Kind: "user_rule",
		Title:   "invoice rounding preference",
		What:    "user wants totals rounded to two decimals",
		Learned: "round every invoice total to two decimals",
	}); err != nil {
		t.Fatalf("save unrelated: %v", err)
	}

	got, err := s.Neighbors(ctx, seed.ID, 0)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("neighbors = %d (%+v), want 1 (related only, unrelated dropped)", len(got), got)
	}
	if got[0].ID != related.ID {
		t.Errorf("neighbor id = %q, want related %q", got[0].ID, related.ID)
	}
	if got[0].ID == seed.ID {
		t.Error("seed returned as its own neighbor")
	}
	if got[0].Score <= 0 {
		t.Errorf("neighbor score = %v, want > 0", got[0].Score)
	}
}

// TestNeighbors_MissingSeedEmpty: an unknown id yields an empty slice, not an error.
func TestNeighbors_MissingSeedEmpty(t *testing.T) {
	s := newTestStore(t)
	got, err := s.Neighbors(context.Background(), "mem_does_not_exist", 0)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("neighbors for missing seed = %+v, want empty", got)
	}
}
