package store_test

import (
	"context"
	"testing"

	"github.com/samuelmolero26/droids-mem/internal/store"
)

func TestPrune_RefusesUnfiltered(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Prune(context.Background(), store.PruneRequest{Apply: true})
	ve := mustValidationError(t, err)
	if ve.Code != "prune_unfiltered" {
		t.Errorf("code = %q, want prune_unfiltered", ve.Code)
	}
}

func TestPrune_RejectsInvalidKind(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Prune(context.Background(), store.PruneRequest{Kind: "observation"})
	ve := mustValidationError(t, err)
	if ve.Code != "invalid_kind" {
		t.Errorf("code = %q, want invalid_kind", ve.Code)
	}
}

func TestPrune_DryRunDoesNotDelete(t *testing.T) {
	s := newTestStore(t)
	seedContextFixture(t, s)

	resp, err := s.Prune(context.Background(), store.PruneRequest{Kind: "error_resolution"})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if resp.Status != "dry_run" {
		t.Errorf("status = %q, want dry_run", resp.Status)
	}
	if resp.Count != 1 {
		t.Fatalf("count = %d, want 1", resp.Count)
	}

	// row must still exist
	again, err := s.Prune(context.Background(), store.PruneRequest{Kind: "error_resolution"})
	if err != nil {
		t.Fatalf("second Prune: %v", err)
	}
	if again.Count != 1 {
		t.Errorf("dry run deleted rows: count = %d, want 1", again.Count)
	}
}

func TestPrune_ApplyDeletesOnlyMatching(t *testing.T) {
	s := newTestStore(t)
	seedContextFixture(t, s) // 1 each of the 4 kinds under crm_upload

	resp, err := s.Prune(context.Background(), store.PruneRequest{Kind: "error_resolution", Apply: true})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if resp.Status != "pruned" || resp.Count != 1 {
		t.Fatalf("status/count = %q/%d, want pruned/1", resp.Status, resp.Count)
	}

	// deleted row gone from FTS too (AD trigger)
	hits, err := s.Search(context.Background(), store.SearchRequest{Query: "phone_number"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, h := range hits.Results {
		if h.Kind == "error_resolution" {
			t.Errorf("pruned error_resolution still searchable: %s", h.ID)
		}
	}

	// other kinds untouched
	left, err := s.Prune(context.Background(), store.PruneRequest{TaskType: "crm_upload"})
	if err != nil {
		t.Fatalf("Prune dry run: %v", err)
	}
	if left.Count != 3 {
		t.Errorf("remaining rows = %d, want 3", left.Count)
	}
}

func TestPrune_OlderThanDays(t *testing.T) {
	s := newTestStore(t)
	seedContextFixture(t, s)

	// nothing is older than 1 day in a fresh store
	resp, err := s.Prune(context.Background(), store.PruneRequest{OlderThanDays: 1})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if resp.Count != 0 {
		t.Errorf("count = %d, want 0 (all rows are fresh)", resp.Count)
	}
}

// seedDupeFamily saves three memories that share most tokens (~0.67 pairwise
// Jaccard): similar enough to cluster at the relaxed 0.6 threshold, distinct
// enough to pass the strict 0.85 save-time near-duplicate gate.
func seedDupeFamily(t *testing.T, s *store.Store) {
	t.Helper()
	family := []store.SaveRequest{
		{
			TaskType: "ci_builds",
			Kind:     "error_resolution",
			Title:    "Flaky integration test timeout alpha",
			What:     "integration suite failed waiting for postgres container startup health probe",
			Learned:  "retry container health probe before failing the suite",
		},
		{
			TaskType: "ci_builds",
			Kind:     "error_resolution",
			Title:    "Flaky integration test timeout bravo",
			What:     "integration suite failed waiting for postgres container network bridge race",
			Learned:  "retry container health probe before failing the suite",
		},
		{
			TaskType: "ci_builds",
			Kind:     "error_resolution",
			Title:    "Flaky integration test timeout charlie",
			What:     "integration suite failed waiting for postgres container disk volume mount",
			Learned:  "retry container health probe before failing the suite",
		},
	}
	for _, m := range family {
		resp, err := s.Save(context.Background(), m)
		if err != nil {
			t.Fatalf("seed dupe family %q: %v", m.Title, err)
		}
		if resp.Status != "saved" {
			t.Fatalf("seed %q not saved (status %q) — family too similar for the 0.85 save gate", m.Title, resp.Status)
		}
	}
}

func TestSuggestDupes_ClustersFamily(t *testing.T) {
	s := newTestStore(t)
	seedDupeFamily(t, s)
	// unrelated row must not join the cluster
	if _, err := s.Save(context.Background(), store.SaveRequest{
		TaskType: "ci_builds",
		Kind:     "error_resolution",
		Title:    "Lint stage missing golangci config",
		What:     "lint job exited because configuration file was absent from repository root",
		Learned:  "commit golangci configuration alongside workflow definition",
	}); err != nil {
		t.Fatalf("seed unrelated: %v", err)
	}

	resp, err := s.SuggestDupes(context.Background(), store.SuggestDupesRequest{})
	if err != nil {
		t.Fatalf("SuggestDupes: %v", err)
	}
	if resp.Threshold != store.DefaultSuggestThreshold {
		t.Errorf("threshold = %v, want default %v", resp.Threshold, store.DefaultSuggestThreshold)
	}
	if resp.RowsScanned != 4 {
		t.Errorf("rows_scanned = %d, want 4", resp.RowsScanned)
	}
	if len(resp.Clusters) != 1 {
		t.Fatalf("clusters = %d, want 1", len(resp.Clusters))
	}
	c := resp.Clusters[0]
	if len(c.Members) != 3 {
		t.Fatalf("cluster members = %d, want 3", len(c.Members))
	}
	seen := map[string]bool{}
	for _, m := range c.Members {
		if seen[m.ID] {
			t.Errorf("member %s appears twice", m.ID)
		}
		seen[m.ID] = true
		if m.ID == c.SeedID {
			if m.Score != 1 {
				t.Errorf("seed score = %v, want 1", m.Score)
			}
		} else if m.Score < store.DefaultSuggestThreshold || m.Score >= 0.85 {
			t.Errorf("member score = %v, want in [0.6, 0.85)", m.Score)
		}
	}
}

func TestSuggestDupes_ConsumedRowsSeedNoSecondCluster(t *testing.T) {
	s := newTestStore(t)
	seedDupeFamily(t, s)

	resp, err := s.SuggestDupes(context.Background(), store.SuggestDupesRequest{})
	if err != nil {
		t.Fatalf("SuggestDupes: %v", err)
	}
	// all three rows consumed by the first cluster — a second run is identical
	// (determinism) and no member appears in more than one cluster
	resp2, err := s.SuggestDupes(context.Background(), store.SuggestDupesRequest{})
	if err != nil {
		t.Fatalf("second SuggestDupes: %v", err)
	}
	if len(resp.Clusters) != 1 || len(resp2.Clusters) != 1 {
		t.Fatalf("clusters = %d/%d, want 1/1", len(resp.Clusters), len(resp2.Clusters))
	}
	if resp.Clusters[0].SeedID != resp2.Clusters[0].SeedID {
		t.Errorf("runs not deterministic: seeds %s vs %s", resp.Clusters[0].SeedID, resp2.Clusters[0].SeedID)
	}
}

func TestSuggestDupes_RejectsBadThreshold(t *testing.T) {
	s := newTestStore(t)
	_, err := s.SuggestDupes(context.Background(), store.SuggestDupesRequest{Threshold: 1.5})
	ve := mustValidationError(t, err)
	if ve.Code != "invalid_threshold" {
		t.Errorf("code = %q, want invalid_threshold", ve.Code)
	}
}

func TestSuggestDupes_StrictThresholdFindsNothing(t *testing.T) {
	s := newTestStore(t)
	seedDupeFamily(t, s)

	resp, err := s.SuggestDupes(context.Background(), store.SuggestDupesRequest{Threshold: 0.99})
	if err != nil {
		t.Fatalf("SuggestDupes: %v", err)
	}
	if len(resp.Clusters) != 0 {
		t.Errorf("clusters = %d, want 0 at threshold 0.99", len(resp.Clusters))
	}
}

func TestPrune_ByID_DryRunThenApply(t *testing.T) {
	s := newTestStore(t)
	saved, err := s.Save(context.Background(), validReq())
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// dry run: matches exactly the one row, deletes nothing
	dry, err := s.Prune(context.Background(), store.PruneRequest{ID: saved.ID})
	if err != nil {
		t.Fatalf("dry-run prune by id: %v", err)
	}
	if dry.Status != "dry_run" || dry.Count != 1 || len(dry.Matched) != 1 || dry.Matched[0].ID != saved.ID {
		t.Fatalf("dry run = %+v, want 1 matched %s", dry, saved.ID)
	}
	if m, _ := s.GetRow(context.Background(), saved.ID); m == nil {
		t.Fatal("dry run deleted the row")
	}

	// apply: row gone
	app, err := s.Prune(context.Background(), store.PruneRequest{ID: saved.ID, Apply: true})
	if err != nil {
		t.Fatalf("apply prune by id: %v", err)
	}
	if app.Status != "pruned" || app.Count != 1 {
		t.Fatalf("apply = %+v, want pruned/1", app)
	}
	if m, _ := s.GetRow(context.Background(), saved.ID); m != nil {
		t.Error("row still present after apply prune by id")
	}
}

func TestPrune_ByID_NoMatchIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	resp, err := s.Prune(context.Background(), store.PruneRequest{ID: "mem_nonexistent", Apply: true})
	if err != nil {
		t.Fatalf("prune missing id: %v", err)
	}
	if resp.Status != "pruned" || resp.Count != 0 {
		t.Errorf("missing-id prune = %+v, want pruned/0", resp)
	}
}

func TestPrune_ByID_IgnoresOtherFilters(t *testing.T) {
	s := newTestStore(t)
	saved, err := s.Save(context.Background(), validReq()) // kind = error_resolution
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Stray non-matching kind + a bogus kind value must NOT block the id delete.
	resp, err := s.Prune(context.Background(), store.PruneRequest{
		ID: saved.ID, Kind: "not_a_kind", TaskType: "some_other_task", Apply: true,
	})
	if err != nil {
		t.Fatalf("prune by id with stray filters: %v", err)
	}
	if resp.Count != 1 {
		t.Errorf("count = %d, want 1 (id wins, filters ignored)", resp.Count)
	}
	if m, _ := s.GetRow(context.Background(), saved.ID); m != nil {
		t.Error("row not deleted — filters wrongly applied")
	}
}
