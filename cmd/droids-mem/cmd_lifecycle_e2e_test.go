package main_test

import (
	"path/filepath"
	"testing"
)

func saveCLI(t *testing.T, dbPath, taskType, kind, title, learned string) string {
	t.Helper()
	out := cli(t, dbPath, nil, "save",
		"--task-type", taskType,
		"--kind", kind,
		"--title", title,
		"--what", "context for "+title,
		"--learned", learned,
	)
	var resp struct {
		Status string `json:"status"`
		ID     string `json:"id"`
	}
	mustParseJSON(t, out, &resp)
	if resp.ID == "" {
		t.Fatalf("save %q returned empty id (status %q)", title, resp.Status)
	}
	return resp.ID
}

// TestE2E_PinAndCap covers the pin CLI contract end-to-end: pin succeeds (exit
// 0), the 6th distinct pin for a task_type is rejected with exit 5, and the
// context bundle surfaces the pinned section.
func TestE2E_PinAndCap(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mem.db")
	topics := []string{
		"authentication token refresh handling",
		"postgres connection pool exhaustion",
		"websocket reconnect backoff strategy",
		"terminal ansi color rendering quirks",
		"filesystem watcher debounce timing",
	}
	for _, tp := range topics {
		id := saveCLI(t, dbPath, "e2e_pincap", "task_pattern", "Lesson on "+tp, "detailed guidance covering "+tp)
		cli(t, dbPath, nil, "pin", "--id", id) // exit 0
	}

	// 6th distinct pin for the same task_type must reject with exit 5 (conflict).
	sixth := saveCLI(t, dbPath, "e2e_pincap", "task_pattern",
		"Sixth lesson on kubernetes pod eviction", "detailed guidance covering kubernetes pod eviction")
	cli(t, dbPath, []int{5}, "pin", "--id", sixth)

	// The context bundle exposes exactly the 5 pinned rows in its pinned section.
	out := cli(t, dbPath, nil, "context", "--task-type", "e2e_pincap")
	var ctx struct {
		Pinned []struct {
			ID     string `json:"id"`
			Pinned bool   `json:"pinned"`
			Tier   string `json:"tier"`
		} `json:"pinned"`
	}
	mustParseJSON(t, out, &ctx)
	if len(ctx.Pinned) != 5 {
		t.Fatalf("expected 5 memories in pinned section, got %d", len(ctx.Pinned))
	}
	for _, p := range ctx.Pinned {
		if !p.Pinned || p.Tier != "always" {
			t.Errorf("pinned item %s: pinned=%v tier=%q, want pinned=true tier=always", p.ID, p.Pinned, p.Tier)
		}
	}
}

// TestE2E_ReviewMarkReviewedExemptRejected covers that mark-reviewed on a
// decay-exempt kind (session_summary) rejects with exit 2, and review list runs.
func TestE2E_ReviewMarkReviewedExemptRejected(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mem.db")
	id := saveCLI(t, dbPath, "e2e_review", "session_summary",
		"Session wrap up", "a detailed account of what happened during this run")

	// session_summary has no decay horizon → mark-reviewed is a usage error (2).
	cli(t, dbPath, []int{2}, "review", "mark-reviewed", "--id", id)

	// review list runs cleanly (exit 0) regardless of contents.
	out := cli(t, dbPath, nil, "review", "list")
	var lst struct {
		Total int `json:"total"`
	}
	mustParseJSON(t, out, &lst)
}

// TestE2E_ArchiveList covers the archive CLI wiring + JSON contract. Supersede
// (which populates the archive) is MCP-only, so via the CLI the archive is
// empty; the store-level supersede test covers population.
func TestE2E_ArchiveList(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mem.db")
	saveCLI(t, dbPath, "e2e_arch", "error_resolution", "Some lesson", "a detailed lesson body about something")

	out := cli(t, dbPath, nil, "archive", "list")
	var resp struct {
		Total    int `json:"total"`
		Memories []struct {
			ID         string `json:"id"`
			ArchivedAt int64  `json:"archived_at"`
		} `json:"memories"`
	}
	mustParseJSON(t, out, &resp)
	if resp.Total != 0 {
		t.Errorf("expected empty archive (no CLI supersede path), got total=%d", resp.Total)
	}
	// --task-type filter also runs cleanly.
	cli(t, dbPath, nil, "archive", "list", "--task-type", "e2e_arch")
}
