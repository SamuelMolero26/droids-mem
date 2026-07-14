package main

import (
	"os"
	"strings"
	"testing"
)

// stripTOMLTable must remove our table through the next top-level header (or
// EOF) and leave everything else — including tables after ours — intact.
func TestStripTOMLTable(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		wantRemoved bool
		want        string
	}{
		{
			name:        "table at EOF, preserves prior content and its newline",
			in:          "model = \"x\"\n\n[mcp_servers.droids-mem]\ncommand = \"/bin/dm\"\nargs = [\"serve\", \"--stdio\"]\n",
			wantRemoved: true,
			want:        "model = \"x\"\n",
		},
		{
			name:        "table followed by another table stops at the next header",
			in:          "[mcp_servers.droids-mem]\ncommand = \"/bin/dm\"\n\n[other]\nk = 1\n",
			wantRemoved: true,
			want:        "[other]\nk = 1\n",
		},
		{
			name:        "marker absent is a no-op",
			in:          "model = \"x\"\n",
			wantRemoved: false,
			want:        "model = \"x\"\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, removed := stripTOMLTable(c.in, codexMCPMarker)
			if removed != c.wantRemoved {
				t.Fatalf("removed = %v, want %v", removed, c.wantRemoved)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// A CLAUDE.md whose block the user edited must not be guessed at — it is left
// in place with manual_removal_needed (ADR-0024 §5).
func TestRemoveClaudeSnippet_EditedBlockLeftAlone(t *testing.T) {
	t.Chdir(t.TempDir()) // --project targets ./CLAUDE.md

	edited := claudeSnippetMarker + "\n\nsome user edit that changed the block\n"
	if err := os.WriteFile("CLAUDE.md", []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	status := removeClaudeSnippetStatus(true)
	if !strings.HasPrefix(status, "manual_removal_needed") {
		t.Fatalf("status = %q, want manual_removal_needed", status)
	}
	b, _ := os.ReadFile("CLAUDE.md")
	if string(b) != edited {
		t.Errorf("edited block was modified:\n%s", b)
	}
}

// append then remove must return CLAUDE.md to its exact prior bytes.
func TestRemoveClaudeSnippet_RoundTrip(t *testing.T) {
	t.Chdir(t.TempDir())

	pre := "# my rules\n"
	if err := os.WriteFile("CLAUDE.md", []byte(pre), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, appended, err := appendClaudeSnippet(true); err != nil || !appended {
		t.Fatalf("append: appended=%v err=%v", appended, err)
	}
	if status := removeClaudeSnippetStatus(true); !strings.HasPrefix(status, "removed") {
		t.Fatalf("remove status = %q, want removed", status)
	}
	b, _ := os.ReadFile("CLAUDE.md")
	if string(b) != pre {
		t.Errorf("round-trip not byte-identical:\ngot  %q\nwant %q", b, pre)
	}

	// Marker now gone -> already_absent.
	if status := removeClaudeSnippetStatus(true); status != "already_absent" {
		t.Errorf("second remove = %q, want already_absent", status)
	}
}
