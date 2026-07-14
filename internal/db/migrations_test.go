package db_test

import (
	"database/sql"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/samuelmolero26/droids-mem/internal/db"
	_ "modernc.org/sqlite"
)

// preV1DDL re-creates the v0 schema (pre-scrub) so we can simulate an
// existing database that needs to be migrated forward. Kept inline here
// — the production schema.go ddl always reflects the latest version.
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

func newPreV1DB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	if _, err := conn.Exec(preV1DDL); err != nil {
		conn.Close()
		t.Fatalf("seed pre-v1 schema: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func userVersion(t *testing.T, conn *sql.DB) int {
	t.Helper()
	var v int
	if err := conn.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	return v
}

func tableColumns(t *testing.T, conn *sql.DB, table string) []string {
	t.Helper()
	rows, err := conn.Query("SELECT name FROM pragma_table_info(?) ORDER BY cid", table)
	if err != nil {
		t.Fatalf("pragma_table_info(%s): %v", table, err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		cols = append(cols, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("pragma_table_info rows: %v", err)
	}
	return cols
}

func tableExists(t *testing.T, conn *sql.DB, name string) bool {
	t.Helper()
	var count int
	if err := conn.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name,
	).Scan(&count); err != nil {
		t.Fatalf("inspect for %s: %v", name, err)
	}
	return count == 1
}

func indexNames(t *testing.T, conn *sql.DB) []string {
	t.Helper()
	rows, err := conn.Query(
		`SELECT name FROM sqlite_master WHERE type='index' AND name NOT LIKE 'sqlite_%'`,
	)
	if err != nil {
		t.Fatalf("inspect indexes: %v", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan index: %v", err)
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("index rows: %v", err)
	}
	sort.Strings(names)
	return names
}

func TestInit_FreshDBStampsUserVersion(t *testing.T) {
	conn := newTestDB(t)
	if got, want := userVersion(t, conn), db.CurrentSchemaVersion; got != want {
		t.Errorf("fresh DB user_version = %d, want %d", got, want)
	}
}

func TestInit_FreshDBHasScrubColumns(t *testing.T) {
	conn := newTestDB(t)
	cols := tableColumns(t, conn, "memories")
	wantCols := map[string]bool{
		"scope":                 false,
		"scrub_pattern_version": false,
		"scrub_counts":          false,
	}
	for _, c := range cols {
		if _, ok := wantCols[c]; ok {
			wantCols[c] = true
		}
	}
	for name, found := range wantCols {
		if !found {
			t.Errorf("fresh DB missing memories.%s", name)
		}
	}
}

func TestInit_FreshDBHasMetaTable(t *testing.T) {
	conn := newTestDB(t)
	if !tableExists(t, conn, "meta") {
		t.Fatal("fresh DB missing meta table")
	}
}

// triggerSQL returns the stored CREATE statement for a trigger.
func triggerSQL(t *testing.T, conn *sql.DB, name string) string {
	t.Helper()
	var sqlText string
	if err := conn.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, name,
	).Scan(&sqlText); err != nil {
		t.Fatalf("read trigger %s sql: %v", name, err)
	}
	return sqlText
}

func hasExpandColumns(cols []string) (hasCount, hasLast bool) {
	for _, c := range cols {
		switch c {
		case "expand_count":
			hasCount = true
		case "last_expanded_at":
			hasLast = true
		}
	}
	return
}

func TestInit_FreshDBHasExpandColumns(t *testing.T) {
	conn := newTestDB(t)
	hasCount, hasLast := hasExpandColumns(tableColumns(t, conn, "memories"))
	if !hasCount {
		t.Error("fresh DB missing memories.expand_count")
	}
	if !hasLast {
		t.Error("fresh DB missing memories.last_expanded_at")
	}
}

// The memories_au trigger must be scoped to the indexed text columns so a
// metadata-only update (the Expand signal increment) does not re-index FTS.
func TestInit_FreshDBScopesUpdateTrigger(t *testing.T) {
	conn := newTestDB(t)
	if sqlText := triggerSQL(t, conn, "memories_au"); !strings.Contains(sqlText, "UPDATE OF") {
		t.Errorf("memories_au not scoped to text columns:\n%s", sqlText)
	}
}

func TestMigrate_V1toV2AddsExpandColumns(t *testing.T) {
	conn := newPreV1DB(t)
	if err := db.Migrate(conn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hasCount, hasLast := hasExpandColumns(tableColumns(t, conn, "memories"))
	if !hasCount {
		t.Error("migrated DB missing memories.expand_count")
	}
	if !hasLast {
		t.Error("migrated DB missing memories.last_expanded_at")
	}
	// Migrate runs the full ladder, so the version lands on the current head.
	if got, want := userVersion(t, conn), db.CurrentSchemaVersion; got != want {
		t.Errorf("post-migrate user_version = %d, want %d", got, want)
	}
}

// A v0 DB carries the original unscoped memories_au trigger; the v1→v2 rung
// must replace it with the scoped form.
func TestMigrate_V1toV2ScopesUpdateTrigger(t *testing.T) {
	conn := newPreV1DB(t)
	if sqlText := triggerSQL(t, conn, "memories_au"); strings.Contains(sqlText, "UPDATE OF") {
		t.Fatalf("pre-migrate trigger already scoped — fixture wrong:\n%s", sqlText)
	}
	if err := db.Migrate(conn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if sqlText := triggerSQL(t, conn, "memories_au"); !strings.Contains(sqlText, "UPDATE OF") {
		t.Errorf("migrated memories_au not scoped:\n%s", sqlText)
	}
}

// The scoped trigger keeps the Expand signal write off the FTS index: bumping
// expand_count must not error and must leave the row findable unchanged.
func TestMigrate_ExpandIncrementDoesNotTouchFTS(t *testing.T) {
	conn := newTestDB(t)
	insertMemory(t, conn, "mem_01", "phone field mapping", "field was wrong", "use phone not phone_number", "hubspot phone")

	if _, err := conn.Exec(`UPDATE memories SET expand_count = expand_count + 1, last_expanded_at = 123 WHERE id = 'mem_01'`); err != nil {
		t.Fatalf("expand increment: %v", err)
	}

	var count int
	var last sql.NullInt64
	if err := conn.QueryRow(`SELECT expand_count, last_expanded_at FROM memories WHERE id = 'mem_01'`).Scan(&count, &last); err != nil {
		t.Fatalf("read expand cols: %v", err)
	}
	if count != 1 || !last.Valid || last.Int64 != 123 {
		t.Errorf("expand columns = (%d, %v), want (1, 123)", count, last)
	}

	var title string
	if err := conn.QueryRow(`
		SELECT m.title FROM memories_fts fts
		JOIN memories m ON m.rowid = fts.rowid
		WHERE memories_fts MATCH 'phone' ORDER BY fts.rank LIMIT 1
	`).Scan(&title); err != nil {
		t.Errorf("FTS search after expand increment: %v", err)
	}
	if title != "phone field mapping" {
		t.Errorf("unexpected title after expand increment: %q", title)
	}
}

func TestMigrate_V0toV1(t *testing.T) {
	conn := newPreV1DB(t)
	if got := userVersion(t, conn); got != 0 {
		t.Fatalf("seed user_version = %d, want 0", got)
	}
	if err := db.Migrate(conn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if got, want := userVersion(t, conn), db.CurrentSchemaVersion; got != want {
		t.Errorf("post-migrate user_version = %d, want %d", got, want)
	}
	cols := tableColumns(t, conn, "memories")
	wantCols := []string{"scope", "scrub_pattern_version", "scrub_counts"}
	for _, want := range wantCols {
		found := false
		for _, c := range cols {
			if c == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("migrated DB missing memories.%s", want)
		}
	}
	if !tableExists(t, conn, "meta") {
		t.Error("migrated DB missing meta table")
	}
}

func TestMigrate_PreservesRows(t *testing.T) {
	conn := newPreV1DB(t)
	now := int64(1000000)
	if _, err := conn.Exec(`
		INSERT INTO memories (id, session_id, task_type, kind, title, what, learned, tags, fingerprint, created_at, updated_at)
		VALUES ('mem_legacy', 'sess_legacy', 'crm', 'task_pattern', 't', 'w', 'l', '', 'fp_legacy', ?, ?)`,
		now, now); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	if err := db.Migrate(conn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	var scope string
	var version int
	var scrubCounts sql.NullString
	if err := conn.QueryRow(
		`SELECT scope, scrub_pattern_version, scrub_counts FROM memories WHERE id = 'mem_legacy'`,
	).Scan(&scope, &version, &scrubCounts); err != nil {
		t.Fatalf("read migrated row: %v", err)
	}
	// The v4→v5 backfill (ADR-0028) reinterprets every pre-v5 row as personal,
	// so a fully-migrated legacy row lands on 'personal' regardless of the
	// v0→v1 column default it first received.
	if scope != "personal" {
		t.Errorf("migrated scope = %q, want 'personal' (ADR-0028 backfill)", scope)
	}
	if version != 1 {
		t.Errorf("scrub_pattern_version default = %d, want 1", version)
	}
	if scrubCounts.Valid {
		t.Errorf("scrub_counts default = %q, want NULL", scrubCounts.String)
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	conn := newPreV1DB(t)
	if err := db.Migrate(conn); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if err := db.Migrate(conn); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if got, want := userVersion(t, conn), db.CurrentSchemaVersion; got != want {
		t.Errorf("user_version = %d, want %d", got, want)
	}
}

func TestInit_IdempotentOnExistingDB(t *testing.T) {
	conn := newPreV1DB(t)
	if err := db.Init(conn); err != nil {
		t.Fatalf("first Init: %v", err)
	}
	if err := db.Init(conn); err != nil {
		t.Fatalf("second Init: %v", err)
	}
	if got, want := userVersion(t, conn), db.CurrentSchemaVersion; got != want {
		t.Errorf("user_version = %d, want %d", got, want)
	}
}

func TestInit_FreshMatchesMigratedShape(t *testing.T) {
	// Phase 1 byte-for-byte equivalence covers what Phase 1 delivers:
	// memories columns, meta table presence, index set. FTS5 tokenizer flip
	// (decision #17) lands in Phase 4 via `migrate --rescrub`, so this test
	// deliberately ignores memories_fts internals.
	fresh := newTestDB(t)
	migrated := newPreV1DB(t)
	if err := db.Migrate(migrated); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if got, want := tableColumns(t, fresh, "memories"), tableColumns(t, migrated, "memories"); !equalStringSlices(got, want) {
		t.Errorf("memories columns diverge:\n fresh   = %v\n migrate = %v", got, want)
	}
	if got, want := tableColumns(t, fresh, "meta"), tableColumns(t, migrated, "meta"); !equalStringSlices(got, want) {
		t.Errorf("meta columns diverge:\n fresh   = %v\n migrate = %v", got, want)
	}
	if got, want := indexNames(t, fresh), indexNames(t, migrated); !equalStringSlices(got, want) {
		t.Errorf("index set diverges:\n fresh   = %v\n migrate = %v", got, want)
	}
}

func TestInit_FreshDBHasOriginColumnAndIndex(t *testing.T) {
	conn := newTestDB(t)
	if !slices.Contains(tableColumns(t, conn, "memories"), "origin") {
		t.Error("fresh DB missing memories.origin")
	}
	if !slices.Contains(indexNames(t, conn), "idx_memories_origin_created") {
		t.Error("fresh DB missing idx_memories_origin_created")
	}
}

// v2→v3 adds origin; existing rows must backfill to 'manual' via the DEFAULT,
// and the recency index must exist after migrate.
func TestMigrate_V2toV3AddsOriginBackfillsManual(t *testing.T) {
	conn := newPreV1DB(t)
	now := int64(1000000)
	if _, err := conn.Exec(`
		INSERT INTO memories (id, session_id, task_type, kind, title, what, learned, tags, fingerprint, created_at, updated_at)
		VALUES ('mem_o', 'sess', 'crm', 'task_pattern', 't', 'w', 'l', '', 'fp_o', ?, ?)`,
		now, now); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	if err := db.Migrate(conn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if !slices.Contains(tableColumns(t, conn, "memories"), "origin") {
		t.Fatal("migrated DB missing memories.origin")
	}
	var origin string
	if err := conn.QueryRow(`SELECT origin FROM memories WHERE id = 'mem_o'`).Scan(&origin); err != nil {
		t.Fatalf("read origin: %v", err)
	}
	if origin != "manual" {
		t.Errorf("backfilled origin = %q, want 'manual'", origin)
	}
	if got, want := userVersion(t, conn), db.CurrentSchemaVersion; got != want {
		t.Errorf("post-migrate user_version = %d, want %d", got, want)
	}
	if !slices.Contains(indexNames(t, conn), "idx_memories_origin_created") {
		t.Error("migrated DB missing idx_memories_origin_created")
	}
}

// A fresh DB carries the file-provenance relation.
func TestInit_FreshDBHasMemoryFilesTable(t *testing.T) {
	conn := newTestDB(t)
	if !tableExists(t, conn, "memory_files") {
		t.Error("fresh DB missing memory_files table")
	}
}

// v3→v4 adds memory_files without disturbing existing memories rows.
func TestMigrate_V3toV4AddsMemoryFiles(t *testing.T) {
	conn := newPreV1DB(t)
	now := int64(1000000)
	if _, err := conn.Exec(`
		INSERT INTO memories (id, session_id, task_type, kind, title, what, learned, tags, fingerprint, created_at, updated_at)
		VALUES ('mem_f', 'sess_f', 'crm', 'task_pattern', 't', 'w', 'l', '', 'fp_f', ?, ?)`,
		now, now); err != nil {
		t.Fatalf("seed row: %v", err)
	}
	if err := db.Migrate(conn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if !tableExists(t, conn, "memory_files") {
		t.Fatal("migrated DB missing memory_files table")
	}
	if got, want := userVersion(t, conn), db.CurrentSchemaVersion; got != want {
		t.Errorf("post-migrate user_version = %d, want %d", got, want)
	}
	var id string
	if err := conn.QueryRow(`SELECT id FROM memories WHERE id = 'mem_f'`).Scan(&id); err != nil {
		t.Errorf("pre-existing row lost after migrate: %v", err)
	}
}

// The CHECK constraint must reject any origin outside {manual, auto} and accept
// 'auto' (the value an Auto-session-summary carries).
func TestOrigin_CheckConstraint(t *testing.T) {
	conn := newTestDB(t)
	if _, err := conn.Exec(`
		INSERT INTO memories (id, session_id, task_type, kind, title, what, learned, tags, fingerprint, created_at, updated_at, origin)
		VALUES ('mem_bad', 'sess', 'crm', 'task_pattern', 't', 'w', 'l', '', 'fp_bad', 1, 1, 'bogus')`,
	); err == nil {
		t.Error("expected CHECK violation for origin='bogus', got nil")
	}
	if _, err := conn.Exec(`
		INSERT INTO memories (id, session_id, task_type, kind, title, what, learned, tags, fingerprint, created_at, updated_at, origin)
		VALUES ('mem_auto', 'sess', 'claude_session', 'session_summary', 't', 'w', 'l', '', 'fp_auto', 1, 1, 'auto')`,
	); err != nil {
		t.Errorf("insert origin='auto' should succeed: %v", err)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
