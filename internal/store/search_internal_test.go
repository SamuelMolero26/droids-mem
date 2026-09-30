package store

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestDedupeTokens_SplitsPunctuationLikeFTS pins that punctuation splits
// tokens (replaced with " ", not ""). Every FTS query builder (ftsOrQuery)
// consumes dedupeTokens terms; a qualified name collapsing into one token
// would never match what FTS5's unicode61 tokenizer indexed as two.
func TestDedupeTokens_SplitsPunctuationLikeFTS(t *testing.T) {
	t.Run("splits_punctuation_like_fts", func(t *testing.T) {
		got, _ := dedupeTokens("store.Save failed on mem_save.Error")

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
	})
}

// TestTokenSet_AgreesWithDedupeTokens guards the fold itself: both helpers now
// project one normalization sweep, so the set must hold exactly the slice's
// terms. Divergence here means someone reintroduced a second sweep.
func TestTokenSet_AgreesWithDedupeTokens(t *testing.T) {
	t.Run("token_set_agrees_with_search_terms", func(t *testing.T) {
		const body = "Fixed the N+1 query in UserList; see store.Search and mem_context."

		terms, _ := dedupeTokens(body)
		set := tokenSet(body)

		if len(terms) != len(set) {
			t.Fatalf("dedupeTokens has %d terms, tokenSet has %d: %v vs %v", len(terms), len(set), terms, set)
		}
		for _, term := range terms {
			if _, ok := set[term]; !ok {
				t.Errorf("term %q missing from tokenSet", term)
			}
		}
	})
}

func TestSnippet_UTF8Safe(t *testing.T) {
	cases := []struct {
		name string
		in   string
		n    int
	}{
		{"ascii_short", "hello world", 120},
		{"ascii_truncate", strings.Repeat("a ", 200), 50},
		{"cjk_truncate", strings.Repeat("中文", 100), 50},
		{"emoji_truncate", strings.Repeat("🔥", 100), 30},
		{"accented_truncate", strings.Repeat("café ", 80), 40},
		{"mixed_truncate", "hello 中文 🔥 café " + strings.Repeat("x", 200), 60},
		{"cut_at_boundary", strings.Repeat("ab", 100), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := snippet(tc.in, tc.n)
			if !utf8.ValidString(got) {
				t.Errorf("snippet produced invalid UTF-8: %q", got)
			}
			runeCount := utf8.RuneCountInString(strings.TrimSuffix(got, "…"))
			if runeCount > tc.n {
				t.Errorf("rune count %d exceeds budget %d (out=%q)", runeCount, tc.n, got)
			}
		})
	}
}
