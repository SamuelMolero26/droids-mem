package main_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestE2E_ScrubCheckRedactsKnownPatterns confirms `scrub --check <file>` runs
// the in-binary engine against an on-disk file and reports the per-pattern
// counts + scrubbed body without touching the database.
func TestE2E_ScrubCheckRedactsKnownPatterns(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "mem.db")
	draftPath := filepath.Join(dir, "draft.md")

	body := "Lesson: rotate the token ghp_" + "abcdefghijklmnopqrstuvwxyz0123456789AB " +
		"that leaked from 192.168.1.42 to alice@example.com.\n"
	if err := os.WriteFile(draftPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write draft: %v", err)
	}

	out := cli(t, dbPath, nil, "scrub", "--check", draftPath)

	var resp struct {
		File           string         `json:"file"`
		RedactionCount int            `json:"redaction_count"`
		PerPattern     map[string]int `json:"per_pattern_counts"`
		PatternVersion int            `json:"pattern_version"`
		Scrubbed       string         `json:"scrubbed"`
	}
	mustParseJSON(t, out, &resp)

	if resp.File != draftPath {
		t.Errorf("file = %q, want %q", resp.File, draftPath)
	}
	if resp.RedactionCount != 3 {
		t.Errorf("redaction_count = %d, want 3", resp.RedactionCount)
	}
	for _, name := range []string{"github_token", "private_ipv4", "email"} {
		if resp.PerPattern[name] != 1 {
			t.Errorf("per_pattern[%s] = %d, want 1", name, resp.PerPattern[name])
		}
	}
	for _, want := range []string{"[GITHUB_TOKEN]", "[PRIVATE_IP]", "[EMAIL]"} {
		if !contains(resp.Scrubbed, want) {
			t.Errorf("scrubbed output missing %q: %q", want, resp.Scrubbed)
		}
	}
	if resp.PatternVersion < 1 {
		t.Errorf("pattern_version = %d, want >= 1", resp.PatternVersion)
	}
}

// TestE2E_ScrubTestSuitePasses runs `scrub --test` end-to-end. The embedded
// fixture corpus must be in the binary and every case must pass; non-zero
// exit signals a regression in the engine.
func TestE2E_ScrubTestSuitePasses(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "mem.db")

	out := cli(t, dbPath, nil, "scrub", "--test")

	var rep struct {
		Total          int `json:"total"`
		Passed         int `json:"passed"`
		Failed         int `json:"failed"`
		PatternVersion int `json:"pattern_version"`
		Cases          []struct {
			Name     string `json:"name"`
			Category string `json:"category"`
			Pass     bool   `json:"pass"`
			Diff     string `json:"diff,omitempty"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(out, &rep); err != nil {
		t.Fatalf("parse: %v\nraw: %s", err, out)
	}

	if rep.Total == 0 {
		t.Fatal("expected at least one corpus case")
	}
	if rep.Failed != 0 {
		for _, c := range rep.Cases {
			if !c.Pass {
				t.Errorf("case %s/%s failed: %s", c.Category, c.Name, c.Diff)
			}
		}
	}
}

// TestE2E_ScrubUsageErrors verifies the mutually-exclusive flag guard. Both
// "neither given" and "both given" should exit ExitUsage without touching
// the embedded corpus.
func TestE2E_ScrubUsageErrors(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "mem.db")

	cli(t, dbPath, []int{2}, "scrub")
	cli(t, dbPath, []int{2}, "scrub", "--check", "/tmp/whatever", "--test")
}

// TestE2E_DoctorScrubStats covers the doctor --scrub-stats path: seed two
// saves (one clean, one with a known secret), then assert the aggregator
// reports the right totals and per-pattern counts.
func TestE2E_DoctorScrubStats(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mem.db")

	cli(t, dbPath, nil, "save",
		"--task-type", "crm_upload",
		"--kind", "task_pattern",
		"--title", "clean lesson title",
		"--what", "Map Phone Number to phone via the staging script.",
		"--learned", "Pin the staging script when the schema changes.",
		"--tags", "hubspot phone",
	)
	cli(t, dbPath, nil, "save",
		"--task-type", "crm_upload",
		"--kind", "task_pattern",
		"--title", "leak after rotation",
		"--what", "Operator reachable at alice@example.com after rotation.",
		"--learned", "Quote the postmortem channel, never paste raw contacts.",
		"--tags", "incident postmortem",
	)

	out := cli(t, dbPath, nil, "doctor", "--scrub-stats")

	var rep struct {
		Status             string         `json:"status"`
		RowsTotal          int            `json:"rows_total"`
		RowsWithRedactions int            `json:"rows_with_redactions"`
		TotalRedactions    int            `json:"total_redactions"`
		PerPattern         map[string]int `json:"per_pattern"`
		PatternVersion     int            `json:"pattern_version"`
	}
	mustParseJSON(t, out, &rep)

	if rep.Status != "ok" {
		t.Errorf("status = %q, want 'ok'", rep.Status)
	}
	if rep.RowsTotal != 2 {
		t.Errorf("rows_total = %d, want 2", rep.RowsTotal)
	}
	if rep.RowsWithRedactions != 1 {
		t.Errorf("rows_with_redactions = %d, want 1", rep.RowsWithRedactions)
	}
	if rep.TotalRedactions != 1 {
		t.Errorf("total_redactions = %d, want 1", rep.TotalRedactions)
	}
	if rep.PerPattern["email"] != 1 {
		t.Errorf("per_pattern[email] = %d, want 1", rep.PerPattern["email"])
	}
	if rep.PatternVersion < 1 {
		t.Errorf("pattern_version = %d, want >= 1", rep.PatternVersion)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
