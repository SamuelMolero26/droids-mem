package main_test

import (
	"path/filepath"
	"testing"
)

func seedPruneRows(t *testing.T, dbPath string) {
	t.Helper()
	rows := [][]string{
		{"--kind", "error_resolution", "--title", "Flaky integration test timeout alpha",
			"--what", "integration suite failed waiting for postgres container startup health probe",
			"--learned", "retry container health probe before failing the suite"},
		{"--kind", "error_resolution", "--title", "Flaky integration test timeout bravo",
			"--what", "integration suite failed waiting for postgres container network bridge race",
			"--learned", "retry container health probe before failing the suite"},
		{"--kind", "task_pattern", "--title", "CSV date normalization",
			"--what", "import fails when dates are not ISO-8601 formatted strings",
			"--learned", "normalize all dates to ISO-8601 before upload"},
	}
	for _, r := range rows {
		args := append([]string{"save", "--task-type", "ci_builds"}, r...)
		cli(t, dbPath, nil, args...)
	}
}

func TestE2E_Prune_DryRunExits10AndKeepsRows(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mem.db")
	seedPruneRows(t, dbPath)

	out := cli(t, dbPath, []int{10}, "prune", "--kind", "error_resolution")
	var resp struct {
		Status string `json:"status"`
		Count  int    `json:"count"`
	}
	mustParseJSON(t, out, &resp)
	if resp.Status != "dry_run" || resp.Count != 2 {
		t.Fatalf("status/count = %q/%d, want dry_run/2", resp.Status, resp.Count)
	}

	// rows survived the dry run
	out = cli(t, dbPath, []int{10}, "prune", "--kind", "error_resolution")
	mustParseJSON(t, out, &resp)
	if resp.Count != 2 {
		t.Fatalf("dry run deleted rows: count = %d, want 2", resp.Count)
	}
}

func TestE2E_Prune_ApplyDeletes(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mem.db")
	seedPruneRows(t, dbPath)

	out := cli(t, dbPath, nil, "prune", "--kind", "error_resolution", "--apply")
	var resp struct {
		Status string `json:"status"`
		Count  int    `json:"count"`
	}
	mustParseJSON(t, out, &resp)
	if resp.Status != "pruned" || resp.Count != 2 {
		t.Fatalf("status/count = %q/%d, want pruned/2", resp.Status, resp.Count)
	}

	out = cli(t, dbPath, []int{10}, "prune", "--task-type", "ci_builds")
	mustParseJSON(t, out, &resp)
	if resp.Count != 1 {
		t.Fatalf("remaining = %d, want 1 (task_pattern only)", resp.Count)
	}
}

func TestE2E_Prune_UnfilteredExits2(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mem.db")
	cli(t, dbPath, []int{2}, "prune", "--apply")
}

func TestE2E_Prune_SuggestDupes(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mem.db")
	seedPruneRows(t, dbPath)

	out := cli(t, dbPath, nil, "prune", "--suggest-dupes")
	var resp struct {
		Status      string  `json:"status"`
		Threshold   float64 `json:"threshold"`
		RowsScanned int     `json:"rows_scanned"`
		Clusters    []struct {
			SeedID  string `json:"seed_id"`
			Members []struct {
				ID    string  `json:"id"`
				Score float64 `json:"score"`
			} `json:"members"`
		} `json:"clusters"`
	}
	mustParseJSON(t, out, &resp)
	if resp.Status != "ok" || resp.RowsScanned != 3 {
		t.Fatalf("status/rows_scanned = %q/%d, want ok/3", resp.Status, resp.RowsScanned)
	}
	if len(resp.Clusters) != 1 {
		t.Fatalf("clusters = %d, want 1 (the two flaky-timeout rows)", len(resp.Clusters))
	}
	if got := len(resp.Clusters[0].Members); got != 2 {
		t.Fatalf("cluster members = %d, want 2", got)
	}
}

func TestE2E_Prune_SuggestDupesWithApplyExits2(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mem.db")
	cli(t, dbPath, []int{2}, "prune", "--suggest-dupes", "--apply")
}
