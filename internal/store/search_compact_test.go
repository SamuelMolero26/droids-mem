package store_test

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/samuelmolero26/droids-mem/internal/store"
)

// TestLearnedPreview locks the compact-row truncation contract: short input
// passes through with no marker, long input is cut at a rune (not byte)
// boundary with a suffix naming the total rune count.
func TestLearnedPreview(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "short learned has no marker",
			input: "Map Phone Number to phone",
			want:  "Map Phone Number to phone",
		},
		{
			name:  "exactly at cap has no marker",
			input: strings.Repeat("a", store.LearnedPreviewMaxRunes),
			want:  strings.Repeat("a", store.LearnedPreviewMaxRunes),
		},
		{
			name:  "over cap truncates with total count",
			input: strings.Repeat("b", 1200),
			want:  strings.Repeat("b", store.LearnedPreviewMaxRunes) + "... (1200 chars total)",
		},
		{
			name:  "unicode truncates at rune boundary with rune count",
			input: strings.Repeat("日", 600),
			want:  strings.Repeat("日", store.LearnedPreviewMaxRunes) + "... (600 chars total)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := store.LearnedPreview(tt.input)
			if got != tt.want {
				t.Errorf("LearnedPreview = %q, want %q", got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("LearnedPreview is not valid UTF-8: %q", got)
			}
		})
	}
}

// TestToCompactSearchResponse is the single proof for the shared list
// projection: allow-listed keys only, lifecycle flags only when true, preview
// applied, total/message passed through, help only when results exist.
func TestToCompactSearchResponse(t *testing.T) {
	resp := &store.SearchResponse{
		Results: []store.SearchResult{
			{
				ID: "mem_01", Kind: "task_pattern", Title: "T", Learned: "short lesson",
				TaskType: "proj", CreatedAt: 123, Score: -1.5, OverlapScore: 0.9,
				ExpandCount: 3, LastExpandedAt: 456, Pinned: true,
			},
			{
				ID: "mem_02", Kind: "task_pattern", Title: "S", Learned: "x",
				TaskType: "proj", NeedsReview: true,
			},
			{
				ID: "mem_03", Kind: "task_pattern", Title: "P", Learned: "x",
				TaskType: "proj",
			},
		},
		Total:   3,
		Message: "some message",
	}

	got := store.ToCompactSearchResponse(resp, "expand me")
	if got.Total != 3 || got.Message != "some message" {
		t.Fatalf("total/message not passed through: %+v", got)
	}
	if len(got.Help) != 1 || got.Help[0] != "expand me" {
		t.Fatalf("help = %v, want the boundary's text when results exist", got.Help)
	}
	if empty := store.ToCompactSearchResponse(&store.SearchResponse{}, "expand me"); len(empty.Help) != 0 {
		t.Fatalf("empty response help = %v, want none", empty.Help)
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if len(decoded.Results) != 3 {
		t.Fatalf("want 3 rows, got %d", len(decoded.Results))
	}
	byID := map[string]map[string]any{}
	for _, r := range decoded.Results {
		byID[r["id"].(string)] = r
	}
	for _, want := range []string{"id", "kind", "title", "task_type", "learned_preview"} {
		if _, ok := byID["mem_01"][want]; !ok {
			t.Errorf("compact row missing %q: %v", want, byID["mem_01"])
		}
	}
	for _, dropped := range []string{"learned", "score", "overlap_score", "expand_count", "created_at", "last_expanded_at"} {
		if _, ok := byID["mem_01"][dropped]; ok {
			t.Errorf("compact row leaks %q: %v", dropped, byID["mem_01"])
		}
	}
	if byID["mem_01"]["learned_preview"] != "short lesson" {
		t.Errorf("preview not applied: %v", byID["mem_01"]["learned_preview"])
	}
	if byID["mem_01"]["pinned"] != true {
		t.Errorf("pinned row lost pinned: %v", byID["mem_01"])
	}
	if byID["mem_02"]["needs_review"] != true {
		t.Errorf("stale row lost needs_review: %v", byID["mem_02"])
	}
	if _, ok := byID["mem_03"]["pinned"]; ok {
		t.Errorf("plain row carries pinned=false: %v", byID["mem_03"])
	}
	if _, ok := byID["mem_03"]["needs_review"]; ok {
		t.Errorf("plain row carries needs_review=false: %v", byID["mem_03"])
	}
}
