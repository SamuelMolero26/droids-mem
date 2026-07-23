package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/samuelmolero26/droids-mem/internal/store"
)

// TestSearch_SurfacesNeedsReviewAndPinned is the [GUARD] for Phase 2: the
// SearchResult projection must carry needs_review/pinned too (D2 — search
// consumers still want the trust signal), computed the same audit-only way
// as Context (D4): it never changes BM25 rank order, only adds the fields.
func TestSearch_SurfacesNeedsReviewAndPinned(t *testing.T) {
	s, conn := newTestStoreWithConn(t)
	taskType := "lifecycle_search"

	saveAndGetID := func(req store.SaveRequest) string {
		resp, err := s.Save(context.Background(), req)
		if err != nil {
			t.Fatalf("seed save: %v", err)
		}
		return resp.ID
	}

	needsReviewID := saveAndGetID(store.SaveRequest{
		TaskType: taskType, Kind: "error_resolution",
		Title: "Phone mapping bug", What: "field mismatch", Learned: "map phone field", Tags: "phone",
	})
	pinnedID := saveAndGetID(store.SaveRequest{
		TaskType: taskType, Kind: "task_pattern",
		Title: "Pinned pattern", What: "csv normalization", Learned: "normalize csv dates", Tags: "csv",
	})
	normalID := saveAndGetID(store.SaveRequest{
		TaskType: taskType, Kind: "user_rule",
		Title: "Plain rule", What: "no marks here", Learned: "nothing special", Tags: "plain",
	})

	past := time.Now().Add(-time.Hour).Unix()
	if _, err := conn.Exec(`UPDATE memories SET review_after = ? WHERE id = ?`, past, needsReviewID); err != nil {
		t.Fatalf("seed review_after: %v", err)
	}
	if _, err := conn.Exec(`UPDATE memories SET pinned = 1 WHERE id = ?`, pinnedID); err != nil {
		t.Fatalf("seed pinned: %v", err)
	}

	resp, err := s.Search(context.Background(), store.SearchRequest{Query: "phone csv plain", TaskType: taskType})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	byID := make(map[string]store.SearchResult, len(resp.Results))
	for _, r := range resp.Results {
		byID[r.ID] = r
	}

	got, ok := byID[needsReviewID]
	if !ok {
		t.Fatal("expected needs-review row in search results")
	}
	if !got.NeedsReview {
		t.Error("NeedsReview = false, want true")
	}
	if got.Pinned {
		t.Error("Pinned = true, want false")
	}

	got, ok = byID[pinnedID]
	if !ok {
		t.Fatal("expected pinned row in search results")
	}
	if !got.Pinned {
		t.Error("Pinned = false, want true")
	}
	if got.NeedsReview {
		t.Error("NeedsReview = true, want false")
	}

	got, ok = byID[normalID]
	if !ok {
		t.Fatal("expected normal row in search results")
	}
	if got.NeedsReview {
		t.Error("normal row NeedsReview = true, want false")
	}
	if got.Pinned {
		t.Error("normal row Pinned = true, want false")
	}
}

func seedMemories(t *testing.T, s *store.Store) {
	t.Helper()
	memories := []store.SaveRequest{
		{
			TaskType: "crm_upload",
			Kind:     "error_resolution",
			Title:    "HubSpot phone field mapping",
			What:     "Upload failed because target field was phone_number",
			Learned:  "Map Phone Number to phone",
			Tags:     "hubspot phone field-mapping",
		},
		{
			TaskType: "crm_upload",
			Kind:     "task_pattern",
			Title:    "CSV date normalization",
			What:     "Import fails when dates are not ISO-8601",
			Learned:  "Normalize all dates to ISO-8601 before upload",
			Tags:     "csv dates iso8601",
		},
		{
			TaskType: "crm_upload",
			Kind:     "user_rule",
			Title:    "Company name abbreviation",
			What:     "User corrected company field format",
			Learned:  "Always abbreviate Company as Co.",
			Tags:     "company abbreviation format",
		},
		{
			TaskType: "email_sync",
			Kind:     "error_resolution",
			Title:    "SMTP auth failure",
			What:     "Email sync failed with 535 auth error",
			Learned:  "Use app password not account password for SMTP",
			Tags:     "smtp auth email password",
		},
	}
	for _, m := range memories {
		if _, err := s.Save(context.Background(), m); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

func TestSearch_ReturnsResults(t *testing.T) {
	s := newTestStore(t)
	seedMemories(t, s)

	resp, err := s.Search(context.Background(), store.SearchRequest{Query: "phone"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Error("expected results for 'phone'")
	}
}

func TestSearch_ScoreIsNegative(t *testing.T) {
	s := newTestStore(t)
	seedMemories(t, s)

	resp, _ := s.Search(context.Background(), store.SearchRequest{Query: "phone"})
	for _, r := range resp.Results {
		if r.Score >= 0 {
			t.Errorf("BM25 score should be negative, got %f for %q", r.Score, r.Title)
		}
	}
}

func TestSearch_FilterByTaskType(t *testing.T) {
	s := newTestStore(t)
	seedMemories(t, s)

	resp, err := s.Search(context.Background(), store.SearchRequest{Query: "auth", TaskType: "email_sync"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, r := range resp.Results {
		if r.TaskType != "email_sync" {
			t.Errorf("expected task_type email_sync, got %q", r.TaskType)
		}
	}
}

func TestSearch_FilterByKind(t *testing.T) {
	s := newTestStore(t)
	seedMemories(t, s)

	resp, err := s.Search(context.Background(), store.SearchRequest{Query: "upload mapping dates", Kind: "task_pattern"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, r := range resp.Results {
		if r.Kind != "task_pattern" {
			t.Errorf("expected kind task_pattern, got %q", r.Kind)
		}
	}
}

func TestSearch_LimitApplied(t *testing.T) {
	s := newTestStore(t)
	seedMemories(t, s)

	resp, _ := s.Search(context.Background(), store.SearchRequest{Query: "upload mapping dates phone smtp", Limit: 2})
	if len(resp.Results) > 2 {
		t.Errorf("expected max 2 results, got %d", len(resp.Results))
	}
}

func TestSearch_DefaultLimit(t *testing.T) {
	s := newTestStore(t)
	seedMemories(t, s)

	resp, _ := s.Search(context.Background(), store.SearchRequest{Query: "upload mapping dates phone smtp"})
	if len(resp.Results) > 5 {
		t.Errorf("default limit should be 5, got %d", len(resp.Results))
	}
}

func TestSearch_LimitCappedAt20(t *testing.T) {
	s := newTestStore(t)
	seedMemories(t, s)

	resp, _ := s.Search(context.Background(), store.SearchRequest{Query: "phone", Limit: 999})
	if len(resp.Results) > 20 {
		t.Errorf("limit should be capped at 20, got %d", len(resp.Results))
	}
}

func TestSearch_NoResults_ReturnsEmptySlice(t *testing.T) {
	s := newTestStore(t)
	seedMemories(t, s)

	resp, err := s.Search(context.Background(), store.SearchRequest{Query: "xyznonexistentterm"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if resp.Results == nil {
		t.Error("expected empty slice, got nil")
	}
	if len(resp.Results) != 0 {
		t.Errorf("expected 0 results, got %d", len(resp.Results))
	}
}

func TestSearch_Validation_EmptyQuery(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Search(context.Background(), store.SearchRequest{Query: ""})
	if err == nil {
		t.Error("expected validation error for empty query")
	}
}

func TestSearch_Validation_InvalidKind(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Search(context.Background(), store.SearchRequest{Query: "phone", Kind: "bad_kind"})
	if err == nil {
		t.Error("expected validation error for invalid kind")
	}
}

func TestSearch_ResultsOrderedByComposite(t *testing.T) {
	s := newTestStore(t)
	seedMemories(t, s)

	resp, _ := s.Search(context.Background(), store.SearchRequest{Query: "phone mapping hubspot", Limit: 5})
	if len(resp.Results) < 2 {
		t.Skip("not enough results to check ordering")
	}
	// Results are re-ranked by CompositeScore (BM25 blended with token overlap),
	// not by raw BM25 rank — so the composite must be non-decreasing.
	for i := 1; i < len(resp.Results); i++ {
		if store.CompositeScore(resp.Results[i]) < store.CompositeScore(resp.Results[i-1]) {
			t.Errorf("results not ordered by composite: [%d]=%f < [%d]=%f",
				i, store.CompositeScore(resp.Results[i]), i-1, store.CompositeScore(resp.Results[i-1]))
		}
	}
}

// TestCompositeScore_OverlapPromotesLowBM25 proves the re-rank is not a no-op:
// a result with a weaker BM25 rank but full token overlap must outrank a
// stronger-BM25 result with zero overlap. Under the old "BM25 primary, overlap
// only as tiebreaker" sort this promotion never happened.
func TestCompositeScore_OverlapPromotesLowBM25(t *testing.T) {
	weakBM25HighOverlap := store.SearchResult{Score: -1.0, OverlapScore: 1.0} // composite -3.0
	strongBM25NoOverlap := store.SearchResult{Score: -2.0, OverlapScore: 0.0} // composite -2.0

	if store.CompositeScore(weakBM25HighOverlap) >= store.CompositeScore(strongBM25NoOverlap) {
		t.Fatalf("high-overlap result did not outrank stronger-BM25 result: %f vs %f",
			store.CompositeScore(weakBM25HighOverlap), store.CompositeScore(strongBM25NoOverlap))
	}
}

func TestSearch_TotalMatchesResultCount(t *testing.T) {
	s := newTestStore(t)
	seedMemories(t, s)

	resp, _ := s.Search(context.Background(), store.SearchRequest{Query: "phone"})
	if resp.Total != len(resp.Results) {
		t.Errorf("total %d != len(results) %d", resp.Total, len(resp.Results))
	}
}

// TestSearch_FTS5SpecialChars guards the regression where FTS5 syntax chars in a
// user query were passed through to MATCH and crashed the parser, e.g.
// `fts5: syntax error near ","`. phraseFTSQuery now quotes every token, so no
// special char can be parsed as query syntax. Each query must parse and still
// surface the seeded "phone mapping" memory.
func TestSearch_FTS5SpecialChars(t *testing.T) {
	s := newTestStore(t)
	seedMemories(t, s)

	queries := []string{
		"phone, mapping",     // comma — the original crash
		"phone: mapping",     // colon — column-filter syntax
		"(phone) mapping",    // parens — grouping syntax
		"phone OR mapping",   // OR keyword as literal text
		"phone NOT mapping",  // NOT keyword as literal text
		"phone NEAR mapping", // NEAR keyword as literal text
		"phone^2 mapping",    // caret
		"phone* mapping",     // trailing wildcard
		`phone "mapping`,     // unbalanced double-quote
		"-phone mapping",     // leading hyphen (old NOT operator)
	}
	for _, q := range queries {
		resp, err := s.Search(context.Background(), store.SearchRequest{Query: q})
		if err != nil {
			t.Errorf("query %q errored (should parse): %v", q, err)
			continue
		}
		if len(resp.Results) == 0 {
			t.Errorf("query %q parsed but matched nothing (expected phone-mapping memory)", q)
		}
	}
}

// TestSearch_OnlyPunctuation returns empty (no tokens) rather than crashing on an
// empty MATCH expression.
func TestSearch_OnlyPunctuation(t *testing.T) {
	s := newTestStore(t)
	seedMemories(t, s)

	resp, err := s.Search(context.Background(), store.SearchRequest{Query: ",,, ::: ()"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if resp.Results == nil {
		t.Error("expected empty slice, got nil")
	}
	if len(resp.Results) != 0 {
		t.Errorf("expected 0 results, got %d", len(resp.Results))
	}
}
