package main

import (
	"encoding/json"
	"os"
	"path/filepath"
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

// A stale session-hook registration (old binary path, e.g. a temp build that
// ran install) must be replaced by the canonical entry, never stacked beside
// it — two live registrations double-count the intake gate. Unrelated user
// hooks in the same event survive.
func TestMergeHooksInto_ReplacesStaleSessionHookEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	stale := map[string]any{
		"hooks": map[string]any{
			"Stop": []any{
				newHookEntry("", "/tmp/scratch/dm session hook"),
				newHookEntry("", "/tmp/scratch/dm session hook"), // duplicate
				map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "codegraph sync"}}},
			},
			"PostToolUse": []any{
				// drifted matcher from an older release
				newHookEntry("Edit|Write|Bash", "/usr/local/bin/droids-mem session hook"),
			},
		},
	}
	b, _ := json.Marshal(stale)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}

	hookCmd := "/usr/local/bin/droids-mem session hook"
	added, err := mergeHooksInto(path, hookCmd)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != len(claudeHookEvents) {
		t.Errorf("all events should be touched, got %v", added)
	}

	out, _ := os.ReadFile(path)
	settings := map[string]any{}
	if err := json.Unmarshal(out, &settings); err != nil {
		t.Fatal(err)
	}
	hooks := settings["hooks"].(map[string]any)

	stop := hooks["Stop"].([]any)
	if len(stop) != 2 { // user's codegraph hook + one canonical entry
		t.Fatalf("Stop should hold user hook + 1 canonical entry, got %d: %s", len(stop), out)
	}
	ours := 0
	for _, e := range stop {
		if entryHasSessionHook(e) {
			ours++
			if !hookEntryExists([]any{e}, hookCmd) {
				t.Errorf("Stop session-hook entry not canonical: %v", e)
			}
		}
	}
	if ours != 1 {
		t.Errorf("Stop holds %d session-hook entries, want 1", ours)
	}

	ptu := hooks["PostToolUse"].([]any)
	if len(ptu) != 1 {
		t.Fatalf("PostToolUse should hold exactly the canonical entry, got %d", len(ptu))
	}
	if m, _ := ptu[0].(map[string]any)["matcher"].(string); m != claudeHookEvents[0].matcher {
		t.Errorf("drifted matcher not canonicalized: %q", m)
	}

	// Re-run: idempotent, nothing touched.
	added, err = mergeHooksInto(path, hookCmd)
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 0 {
		t.Errorf("re-run should touch nothing, got %v", added)
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
