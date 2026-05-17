package store

import (
	"strings"
	"testing"
	"unicode/utf8"
)

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
