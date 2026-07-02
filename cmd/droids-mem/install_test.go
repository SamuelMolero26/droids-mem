package main

import (
	"os"
	"strings"
	"testing"
)

// appendClaudeSnippet must append once and leave an already-snippeted
// CLAUDE.md untouched (the install --all idempotency contract).
func TestAppendClaudeSnippet_Idempotent(t *testing.T) {
	t.Chdir(t.TempDir()) // --project mode targets ./CLAUDE.md

	path, appended, err := appendClaudeSnippet(true)
	if err != nil || !appended {
		t.Fatalf("first append: appended=%v err=%v", appended, err)
	}
	if _, appended, err = appendClaudeSnippet(true); err != nil || appended {
		t.Fatalf("second append must be a no-op: appended=%v err=%v", appended, err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(b), claudeSnippetMarker); got != 1 {
		t.Errorf("snippet marker appears %d times, want 1", got)
	}
}

// A CLAUDE.md with prior unrelated content keeps it and gains the snippet.
func TestAppendClaudeSnippet_PreservesExistingContent(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("CLAUDE.md", []byte("# my rules\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	path, appended, err := appendClaudeSnippet(true)
	if err != nil || !appended {
		t.Fatalf("append: appended=%v err=%v", appended, err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(b), "# my rules\n") || !strings.Contains(string(b), claudeSnippetMarker) {
		t.Errorf("existing content not preserved alongside snippet:\n%s", b)
	}
}
