package main_test

import (
	"path/filepath"
	"strings"
	"testing"
)

// Dry-run must exercise the full save pipeline without persisting anything.
func TestE2E_DryRunDoesNotPersist(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mem.db")

	out := cli(t, dbPath, []int{10}, "save",
		"--task-type", "crm_upload", "--kind", "error_resolution",
		"--title", "Dry run title", "--what", "dry run what", "--learned", "dry run lesson",
		"--dry-run")
	var preview struct {
		Status string `json:"status"`
		Would  string `json:"would"`
	}
	mustParseJSON(t, out, &preview)
	if preview.Status != "dry_run" || preview.Would != "saved" {
		t.Fatalf("preview = %+v, want status=dry_run would=saved", preview)
	}

	listOut := cli(t, dbPath, nil, "list", "--task-type", "crm_upload")
	var list struct {
		Total int `json:"total"`
	}
	mustParseJSON(t, listOut, &list)
	if list.Total != 0 {
		t.Fatalf("dry-run persisted %d memories, want 0", list.Total)
	}

	// Dry-run against an existing duplicate predicts the skip.
	cli(t, dbPath, nil, "save",
		"--task-type", "crm_upload", "--kind", "error_resolution",
		"--title", "Dry run title", "--what", "dry run what", "--learned", "dry run lesson")
	out = cli(t, dbPath, []int{10}, "save",
		"--task-type", "crm_upload", "--kind", "error_resolution",
		"--title", "Dry run title", "--what", "dry run what", "--learned", "dry run lesson",
		"--dry-run")
	mustParseJSON(t, out, &preview)
	if preview.Would != "skipped" {
		t.Fatalf("duplicate dry-run would = %q, want skipped", preview.Would)
	}
}

func TestE2E_TaskTypeCapRejected(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mem.db")

	cli(t, dbPath, []int{2}, "save",
		"--task-type", strings.Repeat("x", 65), "--kind", "task_pattern",
		"--title", "t", "--what", "w", "--learned", "l")
}

func TestE2E_ScopeFlag(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mem.db")

	out := cli(t, dbPath, nil, "save",
		"--task-type", "crm_upload", "--kind", "task_pattern",
		"--title", "Scoped", "--what", "w", "--learned", "l",
		"--scope", "personal")
	var resp struct {
		Status string `json:"status"`
	}
	mustParseJSON(t, out, &resp)
	if resp.Status != "saved" {
		t.Fatalf("status = %q, want saved", resp.Status)
	}

	cli(t, dbPath, []int{2}, "save",
		"--task-type", "crm_upload", "--kind", "task_pattern",
		"--title", "Bad scope", "--what", "w", "--learned", "l",
		"--scope", "global")
}
