package main_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samuelmolero26/droids-mem/internal/state"
)

// bumpChanges sends n meaningful PostToolUse events for the session.
func bumpChanges(t *testing.T, home, db, ccID string, n int) {
	t.Helper()
	for range n {
		sessStdin(t, home, db, `{"hook_event_name":"PostToolUse","session_id":"`+ccID+`","tool_name":"Edit"}`, "session", "hook")
	}
}

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

// PostToolUse: a meaningful tool bumps the change counter; non-meaningful tools
// (Read, Bash) do not.
func TestE2E_HookPostToolUseCountsMeaningfulOnly(t *testing.T) {
	home := t.TempDir()
	db := filepath.Join(t.TempDir(), "mem.db")

	bumpChanges(t, home, db, "cc-h", state.IntakeThreshold)
	sessStdin(t, home, db, `{"hook_event_name":"PostToolUse","session_id":"cc-h","tool_name":"Read"}`, "session", "hook")
	sessStdin(t, home, db, `{"hook_event_name":"PostToolUse","session_id":"cc-h","tool_name":"Bash"}`, "session", "hook")

	var chk struct {
		Count        int  `json:"count"`
		ThresholdMet bool `json:"threshold_met"`
	}
	mustParseJSON(t, sess(t, home, db, "session", "check", "--session", "cc-h"), &chk)
	if chk.Count != state.IntakeThreshold || !chk.ThresholdMet {
		t.Fatalf("expected count %d (Read and Bash ignored), got %+v", state.IntakeThreshold, chk)
	}
}

// Stop: with work accumulated and nothing staged, the hook emits a block
// decision re-prompting the model to stage.
func TestE2E_HookStopBlocksWhenUnstaged(t *testing.T) {
	home := t.TempDir()
	db := filepath.Join(t.TempDir(), "mem.db")

	bumpChanges(t, home, db, "cc-s", state.IntakeThreshold)
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

// Stop with stop_hook_active set: never block, even when a block would
// otherwise fire — re-blocking a turn that is already continuing because of a
// Stop hook loops until the host force-ends it.
func TestE2E_HookStopStandsDownWhenStopHookActive(t *testing.T) {
	home := t.TempDir()
	db := filepath.Join(t.TempDir(), "mem.db")

	bumpChanges(t, home, db, "cc-a", state.IntakeThreshold)
	out := sessStdin(t, home, db, `{"hook_event_name":"Stop","session_id":"cc-a","stop_hook_active":true}`, "session", "hook")
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("Stop with stop_hook_active should be silent, got %q", out)
	}
}

// Staging must satisfy the Stop gate. Only a further IntakeThreshold of
// changes on top of the stage re-arms the block.
func TestE2E_HookStopFreshStageNotStale(t *testing.T) {
	home := t.TempDir()
	db := filepath.Join(t.TempDir(), "mem.db")

	bumpChanges(t, home, db, "cc-f", state.IntakeThreshold)
	stageRun(t, home, db, "cc-f", "checkpointed run")

	out := sessStdin(t, home, db, `{"hook_event_name":"Stop","session_id":"cc-f"}`, "session", "hook")
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("Stop right after staging should be silent, got %q", out)
	}

	// A threshold of new meaningful work makes the stage stale again.
	bumpChanges(t, home, db, "cc-f", state.IntakeThreshold)
	out = sessStdin(t, home, db, `{"hook_event_name":"Stop","session_id":"cc-f"}`, "session", "hook")
	if !strings.Contains(string(out), "block") {
		t.Errorf("Stop after threshold of new changes should block, got %q", out)
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

// File provenance (ADR-0021 Phase 2): PostToolUse captures touched file paths;
// SessionEnd flush records them under the summary's session_id; Bash (no path)
// is ignored.
func TestE2E_HookCapturesFileProvenanceOnFlush(t *testing.T) {
	home := t.TempDir()
	db := filepath.Join(t.TempDir(), "mem.db")

	// Stage with a known droids-mem session_id so we can query provenance after.
	sess(t, home, db, "session", "stage",
		"--session", "cc-p", "--session-id", "sess_prov",
		"--title", "provenance run", "--what", "touched files", "--learned", "remember paths")

	// Read + Edit carry file_path → captured. Bash carries none → ignored, but
	// still bumps the change counter to clear the intake gate.
	sessStdin(t, home, db, `{"hook_event_name":"PostToolUse","session_id":"cc-p","tool_name":"Read","tool_input":{"file_path":"/repo/a.go"}}`, "session", "hook")
	sessStdin(t, home, db, `{"hook_event_name":"PostToolUse","session_id":"cc-p","tool_name":"Edit","tool_input":{"file_path":"/repo/b.go"}}`, "session", "hook")
	for range 3 {
		sessStdin(t, home, db, `{"hook_event_name":"PostToolUse","session_id":"cc-p","tool_name":"Bash"}`, "session", "hook")
	}

	sessStdin(t, home, db, `{"hook_event_name":"SessionEnd","session_id":"cc-p"}`, "session", "hook")

	var fr struct {
		Files []string `json:"files"`
		Total int      `json:"total"`
	}
	mustParseJSON(t, sess(t, home, db, "session", "files", "--session-id", "sess_prov"), &fr)
	if fr.Total != 2 {
		t.Fatalf("provenance files = %+v, want 2 (a.go, b.go; Bash ignored)", fr)
	}
}

// SessionEnd: flushes the staged summary through the gate.
func TestE2E_HookSessionEndFlushes(t *testing.T) {
	home := t.TempDir()
	db := filepath.Join(t.TempDir(), "mem.db")
	stageRun(t, home, db, "cc-e", "the hooked run")
	bumpChanges(t, home, db, "cc-e", state.IntakeThreshold)
	sessStdin(t, home, db, `{"hook_event_name":"SessionEnd","session_id":"cc-e"}`, "session", "hook")

	var rs recentResp
	mustParseJSON(t, sess(t, home, db, "recent-sessions"), &rs)
	if rs.Total != 1 || rs.Sessions[0].Title != "the hooked run" {
		t.Fatalf("SessionEnd hook should have flushed the summary, got %+v", rs)
	}
}
