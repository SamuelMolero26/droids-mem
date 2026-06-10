package db_test

import (
	"database/sql"
	"sort"
	"testing"

	"github.com/samuelmolero/droids-mem/internal/db"
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
	if scope != "shared" {
		t.Errorf("scope default = %q, want 'shared'", scope)
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
