package db_test

import (
	"database/sql"
	"testing"

	"github.com/samuelmolero26/droids-mem/internal/db"
	_ "modernc.org/sqlite"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	if err := db.Init(conn); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestInit_CreatesMemoriesTable(t *testing.T) {
	conn := newTestDB(t)
	var count int
	conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='memories'`).Scan(&count)
	if count != 1 {
		t.Error("memories table missing")
	}
}

func TestInit_CreatesFTSTable(t *testing.T) {
	conn := newTestDB(t)
	var count int
	conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='memories_fts'`).Scan(&count)
	if count != 1 {
		t.Error("memories_fts table missing")
	}
}

func TestInit_CreatesFTSTriggers(t *testing.T) {
	conn := newTestDB(t)
	var count int
	conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name IN ('memories_ai','memories_ad','memories_au')`).Scan(&count)
	if count != 3 {
		t.Errorf("expected 3 FTS triggers, got %d", count)
	}
}

func TestInit_CreatesFingerprintIndex(t *testing.T) {
	conn := newTestDB(t)
	var count int
	conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_memories_fingerprint'`).Scan(&count)
	if count != 1 {
		t.Error("fingerprint unique index missing")
	}
}

func TestInit_Idempotent(t *testing.T) {
	conn := newTestDB(t)
	if err := db.Init(conn); err != nil {
		t.Errorf("second Init call failed: %v", err)
	}
}

func TestFTS_SyncOnInsert(t *testing.T) {
	conn := newTestDB(t)
	insertMemory(t, conn, "mem_01", "phone field mapping", "field was wrong", "use phone not phone_number", "hubspot phone")

	var title string
	err := conn.QueryRow(`
		SELECT m.title FROM memories_fts fts
		JOIN memories m ON m.rowid = fts.rowid
		WHERE memories_fts MATCH 'phone'
		ORDER BY fts.rank LIMIT 1
	`).Scan(&title)
	if err != nil {
		t.Errorf("FTS search after insert: %v", err)
	}
	if title != "phone field mapping" {
		t.Errorf("unexpected title: %q", title)
	}
}

func TestFTS_SyncOnUpdate(t *testing.T) {
	conn := newTestDB(t)
	insertMemory(t, conn, "mem_01", "phone field mapping", "field was wrong", "use phone not phone_number", "hubspot phone")

	conn.Exec(`UPDATE memories SET title = 'updated title' WHERE id = 'mem_01'`)

	var title string
	err := conn.QueryRow(`
		SELECT m.title FROM memories_fts fts
		JOIN memories m ON m.rowid = fts.rowid
		WHERE memories_fts MATCH 'updated'
		ORDER BY fts.rank LIMIT 1
	`).Scan(&title)
	if err != nil {
		t.Errorf("FTS search after update: %v", err)
	}
	if title != "updated title" {
		t.Errorf("unexpected title: %q", title)
	}
}

func TestFTS_SyncOnDelete(t *testing.T) {
	conn := newTestDB(t)
	insertMemory(t, conn, "mem_01", "phone field mapping", "field was wrong", "use phone not phone_number", "hubspot phone")

	conn.Exec(`DELETE FROM memories WHERE id = 'mem_01'`)

	var title string
	err := conn.QueryRow(`
		SELECT m.title FROM memories_fts fts
		JOIN memories m ON m.rowid = fts.rowid
		WHERE memories_fts MATCH 'phone'
		ORDER BY fts.rank LIMIT 1
	`).Scan(&title)
	if err == nil {
		t.Errorf("expected no FTS result after delete, got: %q", title)
	}
}

func TestKindCheck_RejectsInvalid(t *testing.T) {
	conn := newTestDB(t)
	now := int64(1000000)
	_, err := conn.Exec(`
		INSERT INTO memories (id, session_id, task_type, kind, title, what, learned, tags, fingerprint, created_at, updated_at)
		VALUES ('mem_01', 'sess_01', 'crm', 'bad_kind', 'title', 'what', 'learned', '', 'fp1', ?, ?)
	`, now, now)
	if err == nil {
		t.Error("expected CHECK constraint violation for invalid kind")
	}
}

func insertMemory(t *testing.T, conn *sql.DB, id, title, what, learned, tags string) {
	t.Helper()
	now := int64(1000000)
	_, err := conn.Exec(`
		INSERT INTO memories (id, session_id, task_type, kind, title, what, learned, tags, fingerprint, created_at, updated_at)
		VALUES (?, 'sess_01', 'crm', 'error_resolution', ?, ?, ?, ?, ?, ?, ?)
	`, id, title, what, learned, tags, "fp_"+id, now, now)
	if err != nil {
		t.Fatalf("insertMemory %q: %v", id, err)
	}
}
