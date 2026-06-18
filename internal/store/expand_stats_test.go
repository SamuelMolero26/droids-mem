package store_test

import (
	"context"
	"testing"

	"github.com/samuelmolero26/droids-mem/internal/store"
)

func TestExpandStats_RanksByCount(t *testing.T) {
	s := newTestStore(t)
	seedErrors(t, s, 3) // errorSeeds[0..2]: distinct rows

	// Resolve ids via a broad search, then expand them different numbers of times.
	res, err := s.Search(context.Background(), store.SearchRequest{Query: "phone csv rate", Limit: 20})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Results) < 3 {
		t.Fatalf("expected ≥3 seeded errors, got %d", len(res.Results))
	}
	hot := res.Results[0].ID
	mid := res.Results[1].ID
	// hot expanded 3×, mid 1×, third 0×
	for i := 0; i < 3; i++ {
		if _, err := s.Get(context.Background(), hot); err != nil {
			t.Fatalf("Get hot: %v", err)
		}
	}
	if _, err := s.Get(context.Background(), mid); err != nil {
		t.Fatalf("Get mid: %v", err)
	}

	rep, err := s.ExpandStats()
	if err != nil {
		t.Fatalf("ExpandStats: %v", err)
	}
	if rep.RowsTotal != 3 {
		t.Errorf("rows_total = %d, want 3", rep.RowsTotal)
	}
	if rep.RowsExpanded != 2 {
		t.Errorf("rows_expanded = %d, want 2", rep.RowsExpanded)
	}
	if rep.TotalExpansions != 4 {
		t.Errorf("total_expansions = %d, want 4", rep.TotalExpansions)
	}
	if len(rep.TopByCount) != 2 {
		t.Fatalf("top_by_count len = %d, want 2 (only expanded rows)", len(rep.TopByCount))
	}
	if rep.TopByCount[0].ID != hot || rep.TopByCount[0].ExpandCount != 3 {
		t.Errorf("top_by_count[0] = %+v, want %s count 3", rep.TopByCount[0], hot)
	}
	if rep.TopByCount[1].ID != mid {
		t.Errorf("top_by_count[1] = %s, want %s", rep.TopByCount[1].ID, mid)
	}
	for _, e := range rep.TopByRecency {
		if e.LastExpandedAt <= 0 {
			t.Errorf("top_by_recency entry %s has no timestamp", e.ID)
		}
	}
}

func TestExpandStats_EmptyCorpus(t *testing.T) {
	s := newTestStore(t)
	rep, err := s.ExpandStats()
	if err != nil {
		t.Fatalf("ExpandStats: %v", err)
	}
	if rep.RowsExpanded != 0 || rep.TotalExpansions != 0 {
		t.Errorf("empty corpus stats = %+v, want zeros", rep)
	}
	if len(rep.TopByCount) != 0 || len(rep.TopByRecency) != 0 {
		t.Error("empty corpus must yield empty leaderboards")
	}
}
