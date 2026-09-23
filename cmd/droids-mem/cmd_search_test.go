package main

import (
	"strings"
	"testing"

	"github.com/samuelmolero26/droids-mem/internal/store"
)

// TestToSearchListResponse_CLIBoundary is the only CLI-side proof: the scoped
// no-match gains --all-projects syntax (global passes through verbatim) and
// help appears exactly when stub IDs exist. Projection shape is proved once in
// the store (TestToCompactSearchResponse) — never here.
func TestToSearchListResponse_CLIBoundary(t *testing.T) {
	scoped := toSearchListResponse(&store.SearchResponse{
		Results: []store.SearchResult{}, Total: 0, Message: store.NoMatchMessage(true),
	})
	if scoped.Message != store.NoMatchMessage(true)+" Try --all-projects to search every project." {
		t.Errorf("scoped message = %q, want CLI suffix", scoped.Message)
	}
	if len(scoped.Help) != 0 {
		t.Errorf("empty response help = %v, want none", scoped.Help)
	}

	global := toSearchListResponse(&store.SearchResponse{
		Results: []store.SearchResult{}, Total: 0, Message: store.NoMatchMessage(false),
	})
	if global.Message != store.NoMatchMessage(false) {
		t.Errorf("global message = %q, want verbatim pass-through", global.Message)
	}

	withResults := toSearchListResponse(&store.SearchResponse{
		Results: []store.SearchResult{{ID: "a", Learned: "x"}}, Total: 1,
	})
	if len(withResults.Help) != 1 || !strings.Contains(withResults.Help[0], "droids-mem get --id <id>") {
		t.Errorf("help = %v, want get disclosure", withResults.Help)
	}
}
