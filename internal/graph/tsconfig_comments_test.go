package graph

import (
	"encoding/json"
	"testing"
)

// Comment stripping runs before the JSON parse, so anything it drops beyond the
// comment itself corrupts the document. That failure is silent: the parse fails,
// parseAliasConfig returns nil, and the repo's path aliases are quietly lost —
// no error, just an import that stops resolving.
//
// The block-comment cases are the ones that matter here. A line comment ends at
// a newline the stripper emits itself, so it has no character to lose; a block
// comment ends mid-line, and the character after "*/" is real content.
func TestStripJSONComments_KeepsTheCharacterAfterAComment(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "block comment followed by a comma",
			in:   `{"a":1/*c*/,"b":2}`,
			want: `{"a":1,"b":2}`,
		},
		{
			name: "block comment followed by a quote",
			in:   `{/*x*/"a":1}`,
			want: `{"a":1}`,
		},
		{
			name: "block comment surrounded by spaces",
			in:   `{"a":1, /*c*/ "b":2}`,
			want: `{"a":1,  "b":2}`,
		},
		{
			name: "multi-line block comment keeps its newlines",
			in:   "{\"a\":1,/*one\ntwo*/\"b\":2}",
			want: "{\"a\":1,\n\"b\":2}",
		},
		{
			name: "line comment ends at the newline it emits",
			in:   "{\"a\":1 // note\n,\"b\":2}",
			want: "{\"a\":1 \n,\"b\":2}",
		},
		{
			name: "an unterminated block comment consumes the rest",
			in:   `{"a":1/*never closed`,
			want: `{"a":1`,
		},
		{
			name: "comment markers inside a string are content, not comments",
			in:   `{"a":"http://x/*y*/z"}`,
			want: `{"a":"http://x/*y*/z"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(stripJSONComments([]byte(tc.in))); got != tc.want {
				t.Errorf("stripJSONComments(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

// The consequence the stripper exists to avoid: a config that only differs from
// valid JSON by a comment must still parse, and must still yield its aliases.
func TestParseAliasConfig_SurvivesBlockComments(t *testing.T) {
	const cfg = `{
  "compilerOptions": {
    "baseUrl": ".",
    /* path aliases */"paths": {"@/*": ["src/*"]}
  }
}`
	// Guard the premise: without the comment this is ordinary JSON.
	if err := json.Unmarshal([]byte(`{"compilerOptions":{"baseUrl":".","paths":{"@/*":["src/*"]}}}`), &struct{}{}); err != nil {
		t.Fatalf("premise broken: %v", err)
	}

	got := parseAliasConfig([]byte(cfg), t.TempDir(), 0, nil)
	if got == nil {
		t.Fatal("parseAliasConfig returned nil: the comment broke the parse, so every alias in this config is silently lost")
	}
	if want := []string{"src/*"}; len(got.paths["@/*"]) != 1 || got.paths["@/*"][0] != want[0] {
		t.Errorf("paths[\"@/*\"] = %v, want %v", got.paths["@/*"], want)
	}
}
