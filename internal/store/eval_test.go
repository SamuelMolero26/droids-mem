package store_test

import (
	"context"
	"testing"

	"github.com/samuelmolero26/droids-mem/internal/store"
)

// seedEvalCorpus inserts a handful of memories spanning the kinds that matter
// for eligibility: an error_resolution + task_pattern (mem_context-reachable)
// and a session_summary (mem_search-only).
func seedEvalCorpus(t *testing.T, s *store.Store) {
	t.Helper()
	reqs := []store.SaveRequest{
		{
			TaskType: "backend", Kind: "error_resolution",
			Title:   "nil pointer on unchecked map lookup",
			What:    "panic runtime error invalid memory address dereferencing a map value",
			Learned: "check the comma-ok before dereferencing",
			Tags:    "panic nil",
		},
		{
			TaskType: "backend", Kind: "task_pattern",
			Title:   "retry with exponential backoff",
			What:    "transient network failures resolved by retrying with growing delays",
			Learned: "cap the backoff and add jitter",
			Tags:    "retry backoff",
		},
		{
			TaskType: "backend", Kind: "session_summary",
			Title:   "shipped the upload pipeline",
			What:    "built and deployed the csv upload path end to end",
			Learned: "the pipeline is live",
			Tags:    "upload pipeline",
		},
	}
	for _, r := range reqs {
		if _, err := s.Save(context.Background(), r); err != nil {
			t.Fatalf("seed eval corpus: %v", err)
		}
	}
}

func TestEval_ReportShape(t *testing.T) {
	s := newTestStore(t)
	seedEvalCorpus(t, s)

	fixtures := []store.Fixture{
		// morphological hit: "dereferencing" indexes to the same stem as "dereference".
		{Query: "dereference a map value panic", ExpectTitle: "nil pointer on unchecked map lookup", TaskType: "backend", Type: "morphological"},
		// eligible task_pattern, shares "backoff".
		{Query: "exponential backoff for retries", ExpectTitle: "retry with exponential backoff", TaskType: "backend", Type: "reorder"},
		// session_summary target: mem_search-scorable, mem_context-ineligible.
		{Query: "upload pipeline shipped", ExpectTitle: "shipped the upload pipeline", TaskType: "backend", Type: "reorder"},
		// authoring typo: title not in corpus.
		{Query: "anything", ExpectTitle: "no such memory", TaskType: "backend", Type: "synonym"},
	}

	rep, err := s.Eval(context.Background(), fixtures)
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}

	if rep.TotalPairs != 4 {
		t.Errorf("TotalPairs = %d, want 4", rep.TotalPairs)
	}
	if rep.Unresolved != 1 {
		t.Errorf("Unresolved = %d, want 1", rep.Unresolved)
	}
	// 3 resolved pairs scored by mem_search; the unresolved one is excluded.
	if rep.MemSearch.Scored != 3 {
		t.Errorf("MemSearch.Scored = %d, want 3", rep.MemSearch.Scored)
	}
	// Only the error_resolution + task_pattern targets are mem_context-eligible;
	// the session_summary and the unresolved pair are not.
	if rep.MemContext.EligiblePairs != 2 {
		t.Errorf("MemContext.EligiblePairs = %d, want 2", rep.MemContext.EligiblePairs)
	}

	// The three resolved targets each share a token with their query, so all
	// three should surface in mem_search — recall@10 == 1.0.
	if rep.MemSearch.RecallAt10 != 1.0 {
		t.Errorf("MemSearch.RecallAt10 = %.2f, want 1.0", rep.MemSearch.RecallAt10)
	}
	// Both eligible pairs should appear in their task_type's browse tier.
	if rep.MemContext.BrowseHitRate != 1.0 {
		t.Errorf("MemContext.BrowseHitRate = %.2f, want 1.0 (eligible=%d)", rep.MemContext.BrowseHitRate, rep.MemContext.EligiblePairs)
	}

	// Per-pair diagnostics carry through.
	if got := len(rep.Pairs); got != 4 {
		t.Fatalf("len(Pairs) = %d, want 4", got)
	}
	for _, p := range rep.Pairs {
		if p.ExpectTitle == "no such memory" && p.Resolution != "unresolved" {
			t.Errorf("typo pair Resolution = %q, want unresolved", p.Resolution)
		}
	}
}

func TestEval_EmptyFixturesRejected(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Eval(context.Background(), nil); err == nil {
		t.Fatal("Eval(nil) = nil error, want ValidationError")
	}
}
