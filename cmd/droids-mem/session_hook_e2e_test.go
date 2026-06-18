package main_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// sessStdin runs the binary with isolated DB+HOME and a stdin payload (the
// Claude Code hook JSON).
func sessStdin(t *testing.T, home, db, stdin string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(binaryPath, args...)
	cmd.Env = append(os.Environ(), "DROIDS_MEM_DB="+db, "DROIDS_MEM_HOME="+home)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			t.Fatalf("hook %v exited %d (stderr: %s)", args, ee.ExitCode(), ee.Stderr)
		}
		t.Fatalf("hook %v: %v", args, err)
	}
	return out
}

// PostToolUse: a meaningful tool bumps the change counter; a non-meaningful tool
// (Read) does not.
func TestE2E_HookPostToolUseCountsMeaningfulOnly(t *testing.T) {
	home := t.TempDir()
	db := filepath.Join(t.TempDir(), "mem.db")

	for range 3 {
		sessStdin(t, home, db, `{"hook_event_name":"PostToolUse","session_id":"cc-h","tool_name":"Edit"}`, "session", "hook")
	}
	sessStdin(t, home, db, `{"hook_event_name":"PostToolUse","session_id":"cc-h","tool_name":"Read"}`, "session", "hook")

	var chk struct {
		Count        int  `json:"count"`
		ThresholdMet bool `json:"threshold_met"`
	}
	mustParseJSON(t, sess(t, home, db, "session", "check", "--session", "cc-h"), &chk)
	if chk.Count != 3 || !chk.ThresholdMet {
		t.Fatalf("expected count 3 (Read ignored), got %+v", chk)
	}
}

// Stop: with work accumulated and nothing staged, the hook emits a block
// decision re-prompting the model to stage.
func TestE2E_HookStopBlocksWhenUnstaged(t *testing.T) {
	home := t.TempDir()
	db := filepath.Join(t.TempDir(), "mem.db")

	for range 3 {
		sessStdin(t, home, db, `{"hook_event_name":"PostToolUse","session_id":"cc-s","tool_name":"Edit"}`, "session", "hook")
	}
	out := sessStdin(t, home, db, `{"hook_event_name":"Stop","session_id":"cc-s"}`, "session", "hook")
	var dec struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	mustParseJSON(t, out, &dec)
	if dec.Decision != "block" || dec.Reason == "" {
		t.Fatalf("expected block decision, got %+v", dec)
	}
}

// Stop with no work accumulated: no output, no block.
func TestE2E_HookStopSilentWhenQuiet(t *testing.T) {
	home := t.TempDir()
	db := filepath.Join(t.TempDir(), "mem.db")
	out := sessStdin(t, home, db, `{"hook_event_name":"Stop","session_id":"cc-q"}`, "session", "hook")
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("quiet Stop should be silent, got %q", out)
	}
}

// UserPromptSubmit: a relevant prior memory is injected as context text.
func TestE2E_HookUserPromptSubmitInjects(t *testing.T) {
	home := t.TempDir()
	db := filepath.Join(t.TempDir(), "mem.db")
	sess(t, home, db, "save",
		"--task-type", "crm_upload", "--kind", "error_resolution",
		"--title", "HubSpot phone field mapping",
		"--what", "upload failed: field was phone_number",
		"--learned", "map Phone Number to phone",
	)
	out := sessStdin(t, home, db,
		`{"hook_event_name":"UserPromptSubmit","session_id":"cc-u","prompt":"phone field mapping"}`,
		"session", "hook")
	if !strings.Contains(string(out), "phone") {
		t.Fatalf("expected relevant memory injected, got %q", out)
	}
}

// SessionEnd: flushes the staged summary through the gate.
func TestE2E_HookSessionEndFlushes(t *testing.T) {
	home := t.TempDir()
	db := filepath.Join(t.TempDir(), "mem.db")
	stageRun(t, home, db, "cc-e", "the hooked run")
	for range 3 {
		sessStdin(t, home, db, `{"hook_event_name":"PostToolUse","session_id":"cc-e","tool_name":"Bash"}`, "session", "hook")
	}
	sessStdin(t, home, db, `{"hook_event_name":"SessionEnd","session_id":"cc-e"}`, "session", "hook")

	var rs recentResp
	mustParseJSON(t, sess(t, home, db, "recent-sessions"), &rs)
	if rs.Total != 1 || rs.Sessions[0].Title != "the hooked run" {
		t.Fatalf("SessionEnd hook should have flushed the summary, got %+v", rs)
	}
}
