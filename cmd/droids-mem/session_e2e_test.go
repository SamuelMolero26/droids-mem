package main_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// sess runs the binary with both DB and HOME isolated (session state lives under
// DROIDS_MEM_HOME/sessions/). Allows non-zero exits via allowedExits.
func sess(t *testing.T, home, dbPath string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(binaryPath, args...)
	cmd.Env = append(os.Environ(), "DROIDS_MEM_DB="+dbPath, "DROIDS_MEM_HOME="+home)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			t.Fatalf("session %v exited %d (stderr: %s)", args, ee.ExitCode(), ee.Stderr)
		}
		t.Fatalf("session %v: %v", args, err)
	}
	return out
}

type recentResp struct {
	Sessions []struct {
		ID      string `json:"id"`
		Title   string `json:"title"`
		TaskTyp string `json:"task_type"`
	} `json:"sessions"`
	Total int `json:"total"`
}

func stageRun(t *testing.T, home, db, ccID, title string) {
	t.Helper()
	sess(t, home, db, "session", "stage",
		"--session", ccID,
		"--title", title,
		"--what", "did meaningful work on "+title,
		"--learned", "remember the approach for "+title,
	)
}

// Happy path: stage + 3 changes (intake gate met) → flush persists an
// origin='auto' summary that shows up in recent-sessions.
func TestE2E_SessionFlushHappyPath(t *testing.T) {
	home := t.TempDir()
	db := filepath.Join(t.TempDir(), "mem.db")

	stageRun(t, home, db, "cc-happy", "the refactor run")
	for range 3 {
		sess(t, home, db, "session", "mark-change", "--session", "cc-happy")
	}

	var fl struct {
		Flushed   bool   `json:"flushed"`
		ID        string `json:"id"`
		SessionID string `json:"session_id"`
	}
	mustParseJSON(t, sess(t, home, db, "session", "flush", "--session", "cc-happy"), &fl)
	if !fl.Flushed || fl.ID == "" {
		t.Fatalf("expected flushed auto-summary, got %+v", fl)
	}

	var rs recentResp
	mustParseJSON(t, sess(t, home, db, "recent-sessions"), &rs)
	if rs.Total != 1 {
		t.Fatalf("recent-sessions = %+v, want 1 auto summary", rs)
	}
	if rs.Sessions[0].TaskTyp != "claude_session" {
		t.Errorf("default bucket = %q, want claude_session", rs.Sessions[0].TaskTyp)
	}
}

// Intake gate: below the change threshold, flush persists nothing and clears.
func TestE2E_SessionFlushBelowThreshold(t *testing.T) {
	home := t.TempDir()
	db := filepath.Join(t.TempDir(), "mem.db")

	stageRun(t, home, db, "cc-quiet", "trivial run")
	sess(t, home, db, "session", "mark-change", "--session", "cc-quiet") // only 1 < 3

	var fl struct {
		Flushed bool   `json:"flushed"`
		Reason  string `json:"reason"`
	}
	mustParseJSON(t, sess(t, home, db, "session", "flush", "--session", "cc-quiet"), &fl)
	if fl.Flushed || fl.Reason != "below_threshold" {
		t.Fatalf("expected below_threshold no-flush, got %+v", fl)
	}

	var rs recentResp
	mustParseJSON(t, sess(t, home, db, "recent-sessions"), &rs)
	if rs.Total != 0 {
		t.Errorf("quiet session must not persist: total=%d", rs.Total)
	}
}

type pullResp struct {
	Results []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"results"`
	Count int `json:"count"`
}

// Relevance pull: a matching prior memory surfaces once; a second pull in the
// same session is deduped; a strict floor and an unrelated query surface nothing.
func TestE2E_SessionPullRelevanceFloorAndDedupe(t *testing.T) {
	home := t.TempDir()
	db := filepath.Join(t.TempDir(), "mem.db")

	sess(t, home, db, "save",
		"--task-type", "crm_upload",
		"--kind", "error_resolution",
		"--title", "HubSpot phone field mapping",
		"--what", "upload failed because target field was phone_number",
		"--learned", "map Phone Number to phone",
	)

	// Relevant prompt → one hit.
	var p1 pullResp
	mustParseJSON(t, sess(t, home, db, "session", "pull", "--session", "cc-pull", "--query", "phone field mapping"), &p1)
	if p1.Count != 1 || p1.Results[0].Title != "HubSpot phone field mapping" {
		t.Fatalf("expected the phone memory, got %+v", p1)
	}

	// Same session, same prompt → deduped to nothing.
	var p2 pullResp
	mustParseJSON(t, sess(t, home, db, "session", "pull", "--session", "cc-pull", "--query", "phone field mapping"), &p2)
	if p2.Count != 0 {
		t.Errorf("second pull must be deduped, got %d", p2.Count)
	}

	// Fresh session, impossible floor (overlap is capped at 1.0) → the gate
	// rejects even a real match.
	var p3 pullResp
	mustParseJSON(t, sess(t, home, db, "session", "pull", "--session", "cc-strict", "--query", "phone field mapping", "--floor", "1.01"), &p3)
	if p3.Count != 0 {
		t.Errorf("strict floor must reject all, got %d", p3.Count)
	}

	// Unrelated prompt → no FTS match → nothing.
	var p4 pullResp
	mustParseJSON(t, sess(t, home, db, "session", "pull", "--session", "cc-unrel", "--query", "quantum teapot nonsense"), &p4)
	if p4.Count != 0 {
		t.Errorf("unrelated query should surface nothing, got %d", p4.Count)
	}
}

// Recovery: an idle staged file from a "crashed" run (aged past the idle cutoff)
// that meets the gate is flushed on recover; a fresh one is left alone.
func TestE2E_SessionRecoverFlushesIdleOrphan(t *testing.T) {
	home := t.TempDir()
	db := filepath.Join(t.TempDir(), "mem.db")

	// Orphan: staged + 3 changes, then aged so it looks like a crashed run.
	stageRun(t, home, db, "cc-orphan", "crashed run work")
	for range 3 {
		sess(t, home, db, "session", "mark-change", "--session", "cc-orphan")
	}
	old := time.Now().Add(-2 * time.Hour)
	for _, ext := range []string{".staged", ".count"} {
		p := filepath.Join(home, "sessions", "cc-orphan"+ext)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatalf("age %s: %v", p, err)
		}
	}

	// Live: fresh staged file — recover must NOT touch it.
	stageRun(t, home, db, "cc-live", "in-progress run")

	var rec struct {
		Recovered []string `json:"recovered"`
		Swept     []string `json:"swept"`
	}
	mustParseJSON(t, sess(t, home, db, "session", "recover"), &rec)
	if len(rec.Recovered) != 1 || rec.Recovered[0] != "cc-orphan" {
		t.Fatalf("recover should flush the idle orphan, got %+v", rec)
	}

	var rs recentResp
	mustParseJSON(t, sess(t, home, db, "recent-sessions"), &rs)
	if rs.Total != 1 || rs.Sessions[0].Title != "crashed run work" {
		t.Fatalf("recovered summary missing from recent-sessions: %+v", rs)
	}

	// The live session's staged file must still be present (untouched).
	if _, err := os.Stat(filepath.Join(home, "sessions", "cc-live.staged")); err != nil {
		t.Errorf("live session staged file should be left alone: %v", err)
	}
}
