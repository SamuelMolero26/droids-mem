package main_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

var binaryPath string

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "droids-mem-e2e-*")
	if err != nil {
		panic("mktemp: " + err.Error())
	}
	defer os.RemoveAll(tmp)

	binaryPath = filepath.Join(tmp, "droids-mem")
	if out, err := exec.Command("go", "build", "-o", binaryPath, ".").CombinedOutput(); err != nil {
		panic("build failed: " + string(out))
	}

	os.Exit(m.Run())
}

// cli runs the binary with the given DB and args, returns stdout.
// Allows specific exit codes (e.g. 5 for skipped, 10 for dry-run).
func cli(t *testing.T, dbPath string, allowedExits []int, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(binaryPath, args...)
	cmd.Env = append(os.Environ(), "DROIDS_MEM_DB="+dbPath)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code := ee.ExitCode()
			for _, allowed := range allowedExits {
				if code == allowed {
					return out
				}
			}
			t.Fatalf("cli %v exited %d (stderr: %s)", args, code, ee.Stderr)
		}
		t.Fatalf("cli %v: %v", args, err)
	}
	return out
}

// cliStdin runs the binary feeding stdin, for commands like `import`.
func cliStdin(t *testing.T, dbPath string, stdin []byte, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(binaryPath, args...)
	cmd.Env = append(os.Environ(), "DROIDS_MEM_DB="+dbPath)
	cmd.Stdin = bytes.NewReader(stdin)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			t.Fatalf("cli %v exited %d (stderr: %s)", args, ee.ExitCode(), ee.Stderr)
		}
		t.Fatalf("cli %v: %v", args, err)
	}
	return out
}

func mustParseJSON(t *testing.T, data []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("parse JSON: %v\nraw: %s", err, data)
	}
}

func TestE2E_SecondRunUsesFirstRunMemories(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mem.db")

	// ── RUN 1 ────────────────────────────────────────────────────────────────
	// Agent resolves phone field issue, notes user rule, writes session summary.

	cli(t, dbPath, nil, "save",
		"--task-type", "crm_upload",
		"--kind", "error_resolution",
		"--title", "HubSpot phone field mapping",
		"--what", "Upload failed: target field was phone_number",
		"--learned", "Map Phone Number to phone",
		"--tags", "hubspot phone field-mapping",
		"--session-id", "sess_run1",
	)

	cli(t, dbPath, nil, "save",
		"--task-type", "crm_upload",
		"--kind", "user_rule",
		"--title", "Company name abbreviation",
		"--what", "User corrected company field format",
		"--learned", "Always abbreviate Company as Co.",
		"--session-id", "sess_run1",
	)

	cli(t, dbPath, nil, "save",
		"--task-type", "crm_upload",
		"--kind", "session_summary",
		"--title", "Run 1 summary",
		"--what", "CRM upload for client A completed",
		"--learned", "Phone mapping fixed. Company abbreviation rule noted.",
		"--session-id", "sess_run1",
	)

	// ── RUN 2 ────────────────────────────────────────────────────────────────
	// New agent run: load context and verify run 1 memories are present.

	ctxOut := cli(t, dbPath, nil, "context",
		"--task-type", "crm_upload",
		"--query", "phone mapping company",
	)

	var ctxResp struct {
		LastSession *struct {
			Kind  string `json:"kind"`
			Title string `json:"title"`
			Tier  string `json:"tier"`
		} `json:"last_session"`
		UserRules []struct {
			ID    string `json:"id"`
			Kind  string `json:"kind"`
			Title string `json:"title"`
		} `json:"user_rules"`
		Browse []struct {
			ID      string `json:"id"`
			Kind    string `json:"kind"`
			Title   string `json:"title"`
			Snippet string `json:"snippet"`
			Tier    string `json:"tier"`
		} `json:"browse"`
	}
	mustParseJSON(t, ctxOut, &ctxResp)

	if ctxResp.LastSession == nil {
		t.Fatal("run 2: expected last_session from run 1, got nil")
	}
	if ctxResp.LastSession.Kind != "session_summary" {
		t.Errorf("run 2: last_session.kind = %q, want session_summary", ctxResp.LastSession.Kind)
	}
	if len(ctxResp.UserRules) == 0 {
		t.Error("run 2: expected user_rules from run 1 in always tier, got none")
	}
	if len(ctxResp.Browse) == 0 {
		t.Error("run 2: expected browse-tier memories from run 1, got none")
	}

	// Run 2: search for a specific memory from run 1.
	searchOut := cli(t, dbPath, nil, "search",
		"--query", "phone hubspot mapping",
		"--task-type", "crm_upload",
	)

	var searchResp struct {
		Results []struct {
			Title string `json:"title"`
			Kind  string `json:"kind"`
		} `json:"results"`
		Total int `json:"total"`
	}
	mustParseJSON(t, searchOut, &searchResp)

	if searchResp.Total == 0 {
		t.Error("run 2: search found no memories from run 1")
	}

	// Run 2: attempting to re-save the same memory → should be skipped (exit 5).
	cli(t, dbPath, []int{5}, "save",
		"--task-type", "crm_upload",
		"--kind", "error_resolution",
		"--title", "HubSpot phone field mapping",
		"--what", "Upload failed: target field was phone_number",
		"--learned", "Map Phone Number to phone",
		"--session-id", "sess_run2",
	)

	// Run 2: save a genuinely new memory not seen in run 1.
	saveOut := cli(t, dbPath, nil, "save",
		"--task-type", "crm_upload",
		"--kind", "task_pattern",
		"--title", "CSV date normalization",
		"--what", "Import fails when dates are not ISO-8601",
		"--learned", "Normalize all dates to ISO-8601 before upload",
		"--tags", "csv dates iso8601",
		"--session-id", "sess_run2",
	)

	var saveResp struct {
		Status string `json:"status"`
		ID     string `json:"id"`
	}
	mustParseJSON(t, saveOut, &saveResp)

	if saveResp.Status != "saved" {
		t.Errorf("run 2: new memory status = %q, want saved", saveResp.Status)
	}
	if saveResp.ID == "" {
		t.Error("run 2: new memory has empty id")
	}

	// Verify get by ID works for the new memory.
	getOut := cli(t, dbPath, nil, "get", "--id", saveResp.ID)
	var getMem struct {
		ID       string `json:"id"`
		TaskType string `json:"task_type"`
		Kind     string `json:"kind"`
	}
	mustParseJSON(t, getOut, &getMem)
	if getMem.ID != saveResp.ID {
		t.Errorf("get: id mismatch: %q != %q", getMem.ID, saveResp.ID)
	}

	// Final state: list should have all memories from both runs.
	listOut := cli(t, dbPath, nil, "list", "--task-type", "crm_upload")
	var listResp struct {
		Total int `json:"total"`
	}
	mustParseJSON(t, listOut, &listResp)

	// run 1: error_resolution + user_rule + session_summary = 3
	// run 2: task_pattern = 1 (duplicate skipped)
	// total = 4
	if listResp.Total < 4 {
		t.Errorf("expected at least 4 memories after 2 runs, got %d", listResp.Total)
	}
}

// TestE2E_ShareExportImportRoundTrip drives the shared-context surface end to
// end: private-by-default, share grant, export, cross-db import, dedupe, and
// the not-found exit code (ADR-0028).
func TestE2E_ShareExportImportRoundTrip(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src.db")

	saveOut := cli(t, src, nil, "save",
		"--task-type", "go", "--kind", "task_pattern",
		"--title", "use errgroup", "--what", "fan-out",
		"--learned", "errgroup bounds goroutine fan-out",
	)
	var saved struct {
		ID string `json:"id"`
	}
	mustParseJSON(t, saveOut, &saved)

	// A second memory left personal — must never export.
	cli(t, src, nil, "save",
		"--task-type", "go", "--kind", "user_rule",
		"--title", "private note", "--what", "x", "--learned", "keep this local",
	)

	// Export before sharing → empty (private by default).
	if out := cli(t, src, nil, "export"); len(bytes.TrimSpace(out)) != 0 {
		t.Fatalf("export before share leaked rows: %s", out)
	}

	// Share only the errgroup memory.
	cli(t, src, nil, "share", "--id", saved.ID)

	pool := cli(t, src, nil, "export")
	if n := bytes.Count(bytes.TrimSpace(pool), []byte("\n")) + 1; n != 1 {
		t.Fatalf("export = %d lines, want exactly the shared row:\n%s", n, pool)
	}

	// Import into a fresh DB via stdin.
	dst := filepath.Join(t.TempDir(), "dst.db")
	var res struct{ Imported, Skipped, Failed int }
	mustParseJSON(t, cliStdin(t, dst, pool, "import"), &res)
	if res.Imported != 1 || res.Failed != 0 {
		t.Fatalf("import = %+v, want imported=1 failed=0", res)
	}

	// Re-import is idempotent — dedupe skips it.
	mustParseJSON(t, cliStdin(t, dst, pool, "import"), &res)
	if res.Imported != 0 || res.Skipped != 1 {
		t.Fatalf("re-import = %+v, want imported=0 skipped=1", res)
	}

	// Unknown id → exit 3 (not found).
	cli(t, dst, []int{3}, "share", "--id", "mem_does_not_exist")
}
