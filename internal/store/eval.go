package store

import (
	"context"
	"fmt"
	"strings"
)

// Recall eval engine (ADR-0025). Given hand-authored (paraphrased-query →
// expected-memory) Fixture pairs and a seeded store, reports how well retrieval
// survives the vocabulary gap between how a lesson was saved and how it is later
// asked for — the core product claim, otherwise unmeasured. Driven by the
// internal recall benchmark (recall_benchmark_test.go): a fixed embedded corpus
// with distractor clusters, run in CI, results rendered for the README.
//
// The two tools are scored asymmetrically on purpose. mem_search is a single
// BM25-ranked list with a real limit, so recall@k and MRR mean something.
// mem_context's browse tier is a fixed kind-grouped bundle whose per-kind caps
// (10+10) only bind when a task_type holds >10 error/pattern rows, so its
// reported metric is a binary browse_hit_rate over the pairs it can structurally
// reach (error_resolution / task_pattern targets with a task_type). See ADR-0025.

// contextEligibleKinds are the only kinds mem_context's browse tier returns;
// a fixture whose target is any other kind cannot be scored against mem_context.
var contextEligibleKinds = map[string]bool{
	"error_resolution": true,
	"task_pattern":     true,
}

// Fixture is one hand-authored eval pair. Title-keyed rather than ID-keyed so
// the same fixture format is portable across any operator's corpus (ADR-0025).
type Fixture struct {
	Query       string `json:"query"`
	ExpectTitle string `json:"expect_title"`
	TaskType    string `json:"task_type"` // required to score mem_context
	Type        string `json:"type"`      // paraphrase class: synonym|morphological|reorder|abbreviation
}

// PairResult is the per-fixture outcome, surfaced so an operator can eyeball
// which pairs failed and whether a hit was earned by the paraphrase or by
// incidental token overlap (QueryTargetOverlap≈0 on a hit = genuine bridge).
type PairResult struct {
	Query              string  `json:"query"`
	ExpectTitle        string  `json:"expect_title"`
	Type               string  `json:"type"`
	Resolution         string  `json:"resolution"`  // ok | unresolved | ambiguous
	SearchRank         int     `json:"search_rank"` // 1-based position in mem_search results, 0 if absent
	SearchHitAt5       bool    `json:"search_hit_at_5"`
	SearchHitAt10      bool    `json:"search_hit_at_10"`
	ContextEligible    bool    `json:"context_eligible"`
	ContextHit         bool    `json:"context_hit"`
	QueryTargetOverlap float64 `json:"query_target_overlap"`
}

// SearchMetrics scores mem_search: a ranked top-k list, so recall@k and MRR
// both carry signal. Scored counts only resolved ("ok") pairs. recall@1 is the
// harshest headline — the right memory returned first, ahead of every distractor.
type SearchMetrics struct {
	RecallAt1  float64 `json:"recall_at_1"`
	RecallAt5  float64 `json:"recall_at_5"`
	RecallAt10 float64 `json:"recall_at_10"`
	MRR        float64 `json:"mrr"`
	Scored     int     `json:"scored"`
}

// ContextMetrics scores mem_context's browse tier: a fixed bundle, so a binary
// presence rate over the structurally-reachable pairs is the honest metric.
type ContextMetrics struct {
	BrowseHitRate float64 `json:"browse_hit_rate"`
	EligiblePairs int     `json:"eligible_pairs"`
}

// TypeMetrics breaks recall down by paraphrase class — the only way to see that
// synonyms specifically are the gap, not phrasing in general (ADR-0025).
type TypeMetrics struct {
	RecallAt1     float64 `json:"recall_at_1"`     // mem_search, over resolved pairs of this type
	RecallAt5     float64 `json:"recall_at_5"`     // mem_search, over resolved pairs of this type
	MRR           float64 `json:"mrr"`             // mem_search, over resolved pairs of this type
	BrowseHitRate float64 `json:"browse_hit_rate"` // mem_context, over eligible pairs of this type
	N             int     `json:"n"`               // resolved pairs of this type
	EligibleN     int     `json:"eligible_n"`      // mem_context-eligible pairs of this type

	// unexported accumulators (json-ignored); folded into the rates above.
	recallHit1 int
	recallHit5 int
	mrrSum     float64
	browseHits int
}

// EvalReport is report-only — no thresholds, no CI gate (ADR-0025).
type EvalReport struct {
	TotalPairs int                     `json:"total_pairs"`
	Unresolved int                     `json:"unresolved"`
	Ambiguous  int                     `json:"ambiguous"`
	MemSearch  SearchMetrics           `json:"mem_search"`
	MemContext ContextMetrics          `json:"mem_context"`
	ByType     map[string]*TypeMetrics `json:"by_type"`
	Pairs      []PairResult            `json:"pairs"`
}

// Eval runs every Fixture through mem_search (query-only, mirroring real agent
// usage) and, when structurally reachable, mem_context's browse tier, then
// aggregates recall@k / MRR / browse_hit_rate overall and per paraphrase type.
func (s *Store) Eval(ctx context.Context, fixtures []Fixture) (*EvalReport, error) {
	if len(fixtures) == 0 {
		return nil, &ValidationError{Field: "fixtures", Message: "at least one fixture pair required"}
	}

	rep := &EvalReport{
		TotalPairs: len(fixtures),
		ByType:     map[string]*TypeMetrics{},
		Pairs:      make([]PairResult, 0, len(fixtures)),
	}

	// Running tallies (floats derived once at the end).
	var searchHit1, searchHit5, searchHit10, contextHit, eligible int
	var mrrSum float64

	for _, f := range fixtures {
		pr := PairResult{
			Query:       f.Query,
			ExpectTitle: f.ExpectTitle,
			Type:        paraphraseType(f.Type),
		}

		target, resolution, err := s.resolveTarget(ctx, f.ExpectTitle)
		if err != nil {
			return nil, err
		}
		pr.Resolution = resolution
		if resolution != "ok" {
			// A fixture that doesn't cleanly resolve to one row is an authoring
			// problem, not a retrieval failure — reported, excluded from metrics.
			if resolution == "unresolved" {
				rep.Unresolved++
			} else {
				rep.Ambiguous++
			}
			rep.Pairs = append(rep.Pairs, pr)
			continue
		}

		tm := rep.byType(pr.Type)
		tm.N++
		rep.MemSearch.Scored++

		pr.QueryTargetOverlap = TokenOverlap(f.Query,
			target.Title+" "+target.What+" "+target.Learned)

		// mem_search: query-only, no task_type/kind filter (real agent usage).
		rank, err := s.searchRank(ctx, f.Query, target.ID)
		if err != nil {
			return nil, err
		}
		pr.SearchRank = rank
		if rank > 0 {
			rr := 1.0 / float64(rank)
			mrrSum += rr
			tm.mrrSum += rr
			if rank == 1 {
				searchHit1++
				tm.recallHit1++
			}
			if rank <= 5 {
				pr.SearchHitAt5 = true
				searchHit5++
				tm.recallHit5++
			}
			if rank <= 10 {
				pr.SearchHitAt10 = true
				searchHit10++
			}
		}

		// mem_context: only error_resolution/task_pattern targets carrying a
		// task_type are structurally reachable in the browse tier.
		if contextEligibleKinds[target.Kind] && strings.TrimSpace(f.TaskType) != "" {
			pr.ContextEligible = true
			eligible++
			tm.EligibleN++
			hit, err := s.contextBrowseHit(ctx, f.TaskType, f.Query, target.ID)
			if err != nil {
				return nil, err
			}
			pr.ContextHit = hit
			if hit {
				contextHit++
				tm.browseHits++
			}
		}

		rep.Pairs = append(rep.Pairs, pr)
	}

	if scored := rep.MemSearch.Scored; scored > 0 {
		rep.MemSearch.RecallAt1 = ratio(searchHit1, scored)
		rep.MemSearch.RecallAt5 = ratio(searchHit5, scored)
		rep.MemSearch.RecallAt10 = ratio(searchHit10, scored)
		rep.MemSearch.MRR = mrrSum / float64(scored)
	}
	rep.MemContext.EligiblePairs = eligible
	rep.MemContext.BrowseHitRate = ratio(contextHit, eligible)

	for _, tm := range rep.ByType {
		tm.RecallAt1 = ratio(tm.recallHit1, tm.N)
		tm.RecallAt5 = ratio(tm.recallHit5, tm.N)
		if tm.N > 0 {
			tm.MRR = tm.mrrSum / float64(tm.N)
		}
		tm.BrowseHitRate = ratio(tm.browseHits, tm.EligibleN)
	}

	return rep, nil
}

// resolveTarget maps a fixture's expect_title to exactly one Memory. An empty
// match is "unresolved" (typo or pruned since authoring); a multi-row match is
// "ambiguous" (title is not unique) — both benign, both excluded from scoring.
func (s *Store) resolveTarget(ctx context.Context, title string) (*Memory, string, error) {
	rows, err := s.memoriesByTitle(ctx, title)
	if err != nil {
		return nil, "", err
	}
	switch len(rows) {
	case 0:
		return nil, "unresolved", nil
	case 1:
		return &rows[0], "ok", nil
	default:
		return nil, "ambiguous", nil
	}
}

// searchRank returns the 1-based position of targetID in a query-only search
// (up to maxSearchLimit results), or 0 if it never appears.
func (s *Store) searchRank(ctx context.Context, query, targetID string) (int, error) {
	resp, err := s.Search(ctx, SearchRequest{Query: query, Limit: maxSearchLimit})
	if err != nil {
		return 0, err
	}
	for i, r := range resp.Results {
		if r.ID == targetID {
			return i + 1, nil
		}
	}
	return 0, nil
}

// contextBrowseHit reports whether targetID appears anywhere in mem_context's
// orient-mode browse tier for the given task_type.
func (s *Store) contextBrowseHit(ctx context.Context, taskType, query, targetID string) (bool, error) {
	resp, err := s.Context(ctx, ContextRequest{TaskType: taskType, Query: query, Mode: ModeOrient})
	if err != nil {
		return false, err
	}
	for _, m := range resp.Browse {
		if m.ID == targetID {
			return true, nil
		}
	}
	return false, nil
}

// memoriesByTitle returns every memory with an exact (non-FTS) title match.
func (s *Store) memoriesByTitle(ctx context.Context, title string) ([]Memory, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, task_type, kind, title, what, learned, tags, fingerprint, created_at, updated_at
		FROM memories WHERE title = ?
	`, title)
	if err != nil {
		return nil, fmt.Errorf("memories by title: %w", err)
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.ID, &m.SessionID, &m.TaskType, &m.Kind, &m.Title, &m.What, &m.Learned, &m.Tags, &m.Fingerprint, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan memory by title: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// byType lazily creates and returns the per-type accumulator.
func (r *EvalReport) byType(t string) *TypeMetrics {
	tm := r.ByType[t]
	if tm == nil {
		tm = &TypeMetrics{}
		r.ByType[t] = tm
	}
	return tm
}

func paraphraseType(t string) string {
	if strings.TrimSpace(t) == "" {
		return "untyped"
	}
	return t
}

func ratio(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}
