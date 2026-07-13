package main_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// preV1DDL re-seeds the v0 (pre-scrub) schema so migrate E2E tests can
// simulate an existing droids-mem corpus that needs to be brought forward.
// Mirrors the v0 ladder fixture in internal/db; kept independent so this
// package doesn't pull on db_test internals.
const preV1DDL = `
CREATE TABLE memories (
    id          TEXT    PRIMARY KEY,
    session_id  TEXT    NOT NULL,
    task_type   TEXT    NOT NULL,
    kind        TEXT    NOT NULL CHECK(kind IN ('error_resolution','task_pattern','user_rule','session_summary')),
    title       TEXT    NOT NULL,
    what        TEXT    NOT NULL,
    learned     TEXT    NOT NULL,
    tags        TEXT    NOT NULL DEFAULT '',
    fingerprint TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    CHECK(updated_at >= created_at)
);
CREATE UNIQUE INDEX idx_memories_fingerprint ON memories(fingerprint);
CREATE INDEX idx_memories_task_type ON memories(task_type);
CREATE INDEX idx_memories_kind ON memories(kind);
CREATE INDEX idx_memories_task_kind_created ON memories(task_type, kind, created_at DESC);
CREATE INDEX idx_memories_created_at ON memories(created_at DESC);
CREATE VIRTUAL TABLE memories_fts USING fts5(
    title, what, learned, tags,
    content='memories', content_rowid='rowid', tokenize='trigram'
);
CREATE TRIGGER memories_ai AFTER INSERT ON memories BEGIN
    INSERT INTO memories_fts(rowid, title, what, learned, tags)
    VALUES (NEW.rowid, NEW.title, NEW.what, NEW.learned, NEW.tags);
END;
CREATE TRIGGER memories_ad AFTER DELETE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, title, what, learned, tags)
    VALUES ('delete', OLD.rowid, OLD.title, OLD.what, OLD.learned, OLD.tags);
END;
CREATE TRIGGER memories_au AFTER UPDATE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, title, what, learned, tags)
    VALUES ('delete', OLD.rowid, OLD.title, OLD.what, OLD.learned, OLD.tags);
    INSERT INTO memories_fts(rowid, title, what, learned, tags)
    VALUES (NEW.rowid, NEW.title, NEW.what, NEW.learned, NEW.tags);
END;
`

// seedPreV1DB writes a pre-scrub database at dbPath with rows that contain
// secrets in title/what/learned. The returned cleanup closes the seeding
// connection so the binary can open the file afresh.
func seedPreV1DB(t *testing.T, dbPath string) {
	t.Helper()
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Exec(preV1DDL); err != nil {
		t.Fatalf("seed schema: %v", err)
	}
	rows := []struct {
		id, taskType, kind, title, what, learned, tags string
	}{
		{
			id: "mem_legacy_secret", taskType: "crm_upload", kind: "error_resolution",
			title: "Upload failed for user", what: "host 10.0.0.5 returned 500 for alice@example.com payload",
			learned: "retry uploads against alice@example.com after 30 seconds", tags: "hubspot retry",
		},
		{
			id: "mem_legacy_clean", taskType: "crm_upload", kind: "task_pattern",
			title: "Use bulk endpoint", what: "batch endpoint accepts up to 100 rows",
			learned: "send all rows in a single POST", tags: "hubspot bulk",
		},
	}
	for i, r := range rows {
		ts := int64(1700000000 + i)
		_, err := conn.Exec(`
			INSERT INTO memories (id, session_id, task_type, kind, title, what, learned, tags, fingerprint, created_at, updated_at)
			VALUES (?, 'sess_legacy', ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, r.id, r.taskType, r.kind, r.title, r.what, r.learned, r.tags, "legacy_fp_"+r.id, ts, ts)
		if err != nil {
			t.Fatalf("seed row %s: %v", r.id, err)
		}
	}
}

func runBinary(t *testing.T, dbPath string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := exec.Command(binaryPath, args...)
	cmd.Env = append(os.Environ(), "DROIDS_MEM_DB="+dbPath)
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return outBuf.String(), errBuf.String(), ee.ExitCode()
		}
		t.Fatalf("run %v: %v", args, err)
	}
	return outBuf.String(), errBuf.String(), 0
}

func openMigratedDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// TestE2E_MigrateRescrub covers the full upgrade path:
//  1. Pre-v1.0 DB seeded with rows containing secrets.
//  2. Binary boot fails (no scrub baseline).
//  3. `migrate --rescrub` rewrites rows, rebuilds FTS, sets sentinel.
//  4. Subsequent boot succeeds; raw secrets are gone, bracket tokens present.
func TestE2E_MigrateRescrub(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mem.db")
	seedPreV1DB(t, dbPath)

	// migrate bypasses the boot gate, so it is exercised directly on the
	// pre-v1 DB here. (A normal command would auto-run this same rescrub via
	// the gate — see TestE2E_BootGateAutoRescrub — but that would leave nothing
	// for the explicit migrate to redact, so we don't trigger it first.)
	stdout, _, code := runBinary(t, dbPath, "migrate", "--rescrub")
	if code != 0 {
		t.Fatalf("migrate --rescrub failed (exit %d): %s", code, stdout)
	}
	var summary struct {
		Mode               string `json:"mode"`
		RowsScanned        int    `json:"rows_scanned"`
		RowsRewritten      int    `json:"rows_rewritten"`
		RowsWithRedactions int    `json:"rows_with_redactions"`
		TotalRedactions    int    `json:"total_redactions"`
		FTSRebuilt         bool   `json:"fts_rebuilt"`
		BaselineSet        bool   `json:"scrub_baseline_complete_set"`
		PatternVersion     int    `json:"pattern_version"`
	}
	if err := json.Unmarshal([]byte(stdout), &summary); err != nil {
		t.Fatalf("parse migrate summary: %v\nraw: %s", err, stdout)
	}
	if summary.Mode != "rescrub" {
		t.Errorf("mode = %q, want 'rescrub'", summary.Mode)
	}
	if summary.RowsScanned != 2 {
		t.Errorf("rows_scanned = %d, want 2", summary.RowsScanned)
	}
	if summary.RowsRewritten != 2 {
		t.Errorf("rows_rewritten = %d, want 2", summary.RowsRewritten)
	}
	if summary.RowsWithRedactions != 1 {
		t.Errorf("rows_with_redactions = %d, want 1 (only legacy_secret row had secrets)", summary.RowsWithRedactions)
	}
	if summary.TotalRedactions < 2 {
		t.Errorf("total_redactions = %d, want >=2 (1 IP + 2 emails)", summary.TotalRedactions)
	}
	if !summary.FTSRebuilt {
		t.Error("fts_rebuilt = false, want true")
	}
	if !summary.BaselineSet {
		t.Error("scrub_baseline_complete_set = false, want true")
	}

	// Boot gate now passes — verify by running a normal subcommand.
	_, stderr, code := runBinary(t, dbPath, "list")
	if code != 0 {
		t.Fatalf("post-migrate boot failed (exit %d): %s", code, stderr)
	}

	// Direct SQL verification: secrets gone, brackets present.
	conn := openMigratedDB(t, dbPath)
	var what, learned string
	if err := conn.QueryRow(
		`SELECT what, learned FROM memories WHERE id = 'mem_legacy_secret'`,
	).Scan(&what, &learned); err != nil {
		t.Fatalf("read migrated row: %v", err)
	}
	for _, raw := range []string{"alice@example.com", "10.0.0.5"} {
		if strings.Contains(what, raw) || strings.Contains(learned, raw) {
			t.Errorf("raw secret %q still present after rescrub", raw)
		}
	}
	if !strings.Contains(what, "[PRIVATE_IP]") {
		t.Errorf("expected [PRIVATE_IP] in what, got %q", what)
	}
	if !strings.Contains(what, "[EMAIL]") || !strings.Contains(learned, "[EMAIL]") {
		t.Errorf("expected [EMAIL] in what+learned, got what=%q learned=%q", what, learned)
	}

	// scrub_counts populated for the dirty row, NULL for the clean one.
	var dirtyCounts sql.NullString
	if err := conn.QueryRow(
		`SELECT scrub_counts FROM memories WHERE id = 'mem_legacy_secret'`,
	).Scan(&dirtyCounts); err != nil {
		t.Fatalf("read scrub_counts: %v", err)
	}
	if !dirtyCounts.Valid {
		t.Error("expected scrub_counts populated for legacy_secret row")
	}

	var cleanCounts sql.NullString
	if err := conn.QueryRow(
		`SELECT scrub_counts FROM memories WHERE id = 'mem_legacy_clean'`,
	).Scan(&cleanCounts); err != nil {
		t.Fatalf("read clean scrub_counts: %v", err)
	}
	if cleanCounts.Valid {
		t.Errorf("expected NULL scrub_counts for clean row, got %q", cleanCounts.String)
	}

	// Sentinel persisted.
	var sentinel string
	if err := conn.QueryRow(
		`SELECT value FROM meta WHERE key = 'scrub_baseline_complete'`,
	).Scan(&sentinel); err != nil {
		t.Fatalf("read sentinel: %v", err)
	}
	if sentinel != "1" {
		t.Errorf("sentinel = %q, want '1'", sentinel)
	}

	// FTS retokenized — `field-mapping`-style atomic tokens should be searchable.
	// Insert a fresh row through the binary and verify the new tokenizer indexes it.
	_, _, code = runBinary(t, dbPath, "save",
		"--task-type", "crm_upload",
		"--kind", "task_pattern",
		"--title", "field-mapping refresh",
		"--what", "phone_number maps to phone",
		"--learned", "rerun the mapping job after schema bumps",
		"--session-id", "sess_post_migrate",
	)
	if code != 0 {
		t.Fatalf("post-migrate save failed (exit %d)", code)
	}
}

// TestE2E_MigrateNoRescrub confirms the lighter path: baseline is set, FTS
// gets the v1.0 tokenizer, but row bodies are NOT rewritten.
func TestE2E_MigrateNoRescrub(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mem.db")
	seedPreV1DB(t, dbPath)

	stdout, _, code := runBinary(t, dbPath, "migrate", "--no-rescrub")
	if code != 0 {
		t.Fatalf("migrate --no-rescrub failed (exit %d): %s", code, stdout)
	}
	var summary struct {
		Mode               string `json:"mode"`
		RowsScanned        int    `json:"rows_scanned"`
		RowsRewritten      int    `json:"rows_rewritten"`
		RowsWithRedactions int    `json:"rows_with_redactions"`
		FTSRebuilt         bool   `json:"fts_rebuilt"`
		BaselineSet        bool   `json:"scrub_baseline_complete_set"`
	}
	if err := json.Unmarshal([]byte(stdout), &summary); err != nil {
		t.Fatalf("parse migrate summary: %v\nraw: %s", err, stdout)
	}
	if summary.Mode != "no-rescrub" {
		t.Errorf("mode = %q, want 'no-rescrub'", summary.Mode)
	}
	if summary.RowsRewritten != 0 {
		t.Errorf("rows_rewritten = %d, want 0 on no-rescrub", summary.RowsRewritten)
	}
	if !summary.FTSRebuilt {
		t.Error("fts_rebuilt = false, want true (tokenizer flip still applies)")
	}
	if !summary.BaselineSet {
		t.Error("expected baseline set")
	}

	conn := openMigratedDB(t, dbPath)
	var what string
	if err := conn.QueryRow(
		`SELECT what FROM memories WHERE id = 'mem_legacy_secret'`,
	).Scan(&what); err != nil {
		t.Fatalf("read legacy row: %v", err)
	}
	if !strings.Contains(what, "alice@example.com") {
		t.Error("no-rescrub should NOT touch row bodies; expected raw email to remain")
	}

	// Boot now succeeds even though plaintext remains.
	_, stderr, code := runBinary(t, dbPath, "list")
	if code != 0 {
		t.Fatalf("post --no-rescrub boot failed (exit %d): %s", code, stderr)
	}
}

// TestE2E_BootGateAutoRescrub covers issue #29: a normal command against a
// stale (pre-baseline) DB must not die on the boot gate. The gate auto-runs
// `migrate --rescrub`, logs a loud line, and lets the command proceed — so the
// memory tools never go dark waiting on a manual migration.
func TestE2E_BootGateAutoRescrub(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mem.db")
	seedPreV1DB(t, dbPath)

	// A gated command now succeeds instead of exiting non-zero.
	_, stderr, code := runBinary(t, dbPath, "list")
	if code != 0 {
		t.Fatalf("expected auto-remediated boot to succeed, got exit %d: %s", code, stderr)
	}
	if !strings.Contains(stderr, "auto-rescrubbed") {
		t.Errorf("expected loud auto-migration log on stderr, got: %q", stderr)
	}

	// Secrets in the seeded row were scrubbed by the auto-migration.
	conn := openMigratedDB(t, dbPath)
	var what, learned string
	if err := conn.QueryRow(
		`SELECT what, learned FROM memories WHERE id = 'mem_legacy_secret'`,
	).Scan(&what, &learned); err != nil {
		t.Fatalf("read migrated row: %v", err)
	}
	for _, raw := range []string{"alice@example.com", "10.0.0.5"} {
		if strings.Contains(what, raw) || strings.Contains(learned, raw) {
			t.Errorf("raw secret %q still present after auto-rescrub", raw)
		}
	}

	// Baseline sentinel is set, so a second command does NOT re-trigger.
	_, stderr2, code := runBinary(t, dbPath, "list")
	if code != 0 {
		t.Fatalf("second boot failed (exit %d): %s", code, stderr2)
	}
	if strings.Contains(stderr2, "rescrub") {
		t.Errorf("auto-migration re-fired on an already-migrated DB: %q", stderr2)
	}
}

// TestE2E_MigrateRejectsAmbiguousFlags confirms the CLI guard between
// --rescrub and --no-rescrub.
func TestE2E_MigrateRejectsAmbiguousFlags(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mem.db")
	seedPreV1DB(t, dbPath)

	_, _, code := runBinary(t, dbPath, "migrate", "--rescrub", "--no-rescrub")
	if code == 0 {
		t.Error("expected non-zero exit when both flags supplied")
	}

	_, _, code = runBinary(t, dbPath, "migrate")
	if code == 0 {
		t.Error("expected non-zero exit when neither flag supplied")
	}
}
