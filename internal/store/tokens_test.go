package store

import "testing"

// TestSearchTerms_SplitsPunctuationLikeFTS pins the invariant that folding
// searchTerms into dedupeTokens established. dupeQuery (prune.go) documents
// itself as building "the same capped, phrase-quoted OR query the save-time
// near-duplicate check uses", but save-time runs on dedupeTokens while
// searchTerms used to strip punctuation to "" instead of " ". A qualified
// name collapsed into one token that FTS5's unicode61 tokenizer had indexed
// as two, so the phrase-quoted term could never match.
func TestSearchTerms_SplitsPunctuationLikeFTS(t *testing.T) {
	got := searchTerms("store.Save failed on mem_save.Error")

	want := map[string]bool{"store": true, "save": true, "failed": true, "mem_save": true, "error": true}
	for _, term := range got {
		if !want[term] {
			t.Errorf("unexpected term %q in %v", term, got)
		}
		delete(want, term)
	}
	for term := range want {
		t.Errorf("missing term %q in %v", term, got)
	}
}

// TestTokenSet_AgreesWithSearchTerms guards the fold itself: both helpers now
// project one normalization sweep, so the set must hold exactly the slice's
// terms. Divergence here means someone reintroduced a second sweep.
func TestTokenSet_AgreesWithSearchTerms(t *testing.T) {
	const body = "Fixed the N+1 query in UserList; see store.Search and mem_context."

	terms := searchTerms(body)
	set := tokenSet(body)

	if len(terms) != len(set) {
		t.Fatalf("searchTerms has %d terms, tokenSet has %d: %v vs %v", len(terms), len(set), terms, set)
	}
	for _, term := range terms {
		if _, ok := set[term]; !ok {
			t.Errorf("term %q missing from tokenSet", term)
		}
	}
}
