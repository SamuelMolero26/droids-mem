package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samuelmolero26/droids-mem/internal/db"
	"github.com/samuelmolero26/droids-mem/internal/store"
	_ "modernc.org/sqlite"
)

// loadFixtureStore replays a golden per-version schema fixture from
// internal/db/testdata (relative to this package's CWD) into an in-memory DB
// WITHOUT running the ladder — the test decides when to migrate.
func loadFixtureStore(t *testing.T, name string) *sql.DB {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "db", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	if _, err := conn.Exec(string(raw)); err != nil {
		conn.Close()
		t.Fatalf("seed fixture %s: %v", name, err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func userVersionOn(t *testing.T, conn *sql.DB) int {
	t.Helper()
	var v int
	if err := conn.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	return v
}

// TestMigrate_RejectsNonCurrentVersion proves SM-R2's precondition: a
// database whose user_version is not CurrentSchemaVersion must be refused
// loudly BEFORE any write. The ladder owns schema shape (including the rung
// 7→8 FTS flip); store.Migrate only performs the optional rescrub rewrite and
// the sentinel stamp and must not run against a schema the ladder has not
// brought current yet.
func TestMigrate_RejectsNonCurrentVersion(t *testing.T) {
	conn := loadFixtureStore(t, "schema_v7.sql")
	s := store.New(conn)

	_, err := store.Migrate(t.Context(), s, store.MigrateOptions{Rescrub: true})
	var svErr *store.SchemaVersionError
	if !errors.As(err, &svErr) {
		t.Fatalf("Migrate on a v7 DB: want *store.SchemaVersionError, got %v", err)
	}
	if svErr.Current != 7 || svErr.Required != db.CurrentSchemaVersion {
		t.Errorf("SchemaVersionError = {%d, %d}, want {%d, %d}",
			svErr.Current, svErr.Required, 7, db.CurrentSchemaVersion)
	}

	// No write happened: no sentinel, version untouched.
	var n int
	if err := conn.QueryRow(
		`SELECT COUNT(*) FROM meta WHERE key = 'scrub_baseline_complete'`,
	).Scan(&n); err != nil {
		t.Fatalf("count meta: %v", err)
	}
	if n != 0 {
		t.Errorf("sentinel written by a rejected Migrate (%d rows)", n)
	}
	if got := userVersionOn(t, conn); got != 7 {
		t.Errorf("user_version = %d, want 7 (unchanged)", got)
	}
}

// TestMigrate_RescrubCollisionFailsLoud proves SM-R5: two rows whose
// POST-scrub fingerprints collide must abort --rescrub with a typed error
// naming the offending row and the collision partner, before any UPDATE — no
// sentinel, no partial rewrite, no auto-merge.
func TestMigrate_RescrubCollisionFailsLoud(t *testing.T) {
	conn := loadFixtureStore(t, "schema_v0.sql")
	now := int64(1000000)
	// Distinct pre-scrub titles (distinct fingerprints at seed time) that
	// scrub to the SAME token stream: alice@example.com / bob@example.com →
	// [EMAIL]. Same task_type + kind + learned → same post-scrub fingerprint.
	seed := []struct{ id, title string }{
		{id: "mem_coll_a", title: "retry alice@example.com"},
		{id: "mem_coll_b", title: "retry bob@example.com"},
	}
	for _, r := range seed {
		if _, err := conn.Exec(`
			INSERT INTO memories (id, session_id, task_type, kind, title, what, learned, tags, fingerprint, created_at, updated_at)
			VALUES (?, 'sess', 'crm_upload', 'error_resolution', ?, 'row ' || ?, 'handle it the same way', '', 'fp_' || ?, ?, ?)`,
			r.id, r.title, r.id, r.id, now, now); err != nil {
			t.Fatalf("seed %s: %v", r.id, err)
		}
	}
	if err := db.Migrate(conn); err != nil {
		t.Fatalf("Migrate ladder: %v", err)
	}

	s := store.New(conn)
	_, err := store.Migrate(t.Context(), s, store.MigrateOptions{Rescrub: true})
	var fpc *store.FingerprintCollisionError
	if !errors.As(err, &fpc) {
		t.Fatalf("Migrate --rescrub on colliding rows: want *store.FingerprintCollisionError, got %v", err)
	}
	if fpc.RowID != "mem_coll_b" {
		t.Errorf("RowID = %q, want %q (the second row to re-fingerprint)", fpc.RowID, "mem_coll_b")
	}
	if fpc.CollidesWith != "mem_coll_a" {
		t.Errorf("CollidesWith = %q, want %q (the first row holding the fingerprint)", fpc.CollidesWith, "mem_coll_a")
	}
	if fpc.Fingerprint == "" {
		t.Error("Fingerprint empty — the error must name the colliding fingerprint")
	}

	// Full rollback: no sentinel, and no row body was rewritten.
	var n int
	if err := conn.QueryRow(
		`SELECT COUNT(*) FROM meta WHERE key = 'scrub_baseline_complete'`,
	).Scan(&n); err != nil {
		t.Fatalf("count meta: %v", err)
	}
	if n != 0 {
		t.Errorf("sentinel persisted despite collision abort (%d rows)", n)
	}
	var title string
	if err := conn.QueryRow(`SELECT title FROM memories WHERE id = 'mem_coll_b'`).Scan(&title); err != nil {
		t.Fatalf("read mem_coll_b: %v", err)
	}
	if !strings.Contains(title, "bob@example.com") {
		t.Errorf("row body rewritten despite abort: %q (raw email must survive rollback)", title)
	}
}

// TestMigrate_HonorsContextCancellation proves the rescrub is interruptible.
// It is the longest-running write in the binary — a full row rewrite over the
// whole corpus — and it runs unattended from the boot gate's auto-migration,
// so a caller that gives up (Ctrl-C, a serve shutdown, a request deadline)
// must actually stop it. Cancelling must also leave nothing behind: the work
// is inside BEGIN IMMEDIATE, so an aborted run rolls back to the pre-migrate
// shape with no sentinel and the boot gate still closed.
func TestMigrate_HonorsContextCancellation(t *testing.T) {
	conn := loadFixtureStore(t, "schema_v0.sql")
	now := int64(1000000)
	for i := range 3 {
		if _, err := conn.Exec(`
			INSERT INTO memories (id, session_id, task_type, kind, title, what, learned, tags, fingerprint, created_at, updated_at)
			VALUES (?, 'sess', 'crm_upload', 'error_resolution', ?, 'body', 'lesson ' || ?, '', ?, ?, ?)`,
			fmt.Sprintf("mem_ctx_%d", i), fmt.Sprintf("row %d", i), i,
			fmt.Sprintf("fp_%d", i), now, now); err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}
	if err := db.Migrate(conn); err != nil {
		t.Fatalf("Migrate ladder: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // caller already gave up before the first statement runs

	s := store.New(conn)
	_, err := store.Migrate(ctx, s, store.MigrateOptions{Rescrub: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Migrate with a cancelled context: want context.Canceled, got %v", err)
	}

	// Nothing committed: no sentinel, so the boot gate stays closed and the
	// operator can retry.
	var n int
	if err := conn.QueryRow(
		`SELECT COUNT(*) FROM meta WHERE key = 'scrub_baseline_complete'`,
	).Scan(&n); err != nil {
		t.Fatalf("count meta: %v", err)
	}
	if n != 0 {
		t.Errorf("sentinel persisted despite cancellation (%d rows)", n)
	}
	var bgErr *db.BootGateError
	if err := db.AssertBootReady(conn); !errors.As(err, &bgErr) {
		t.Errorf("boot gate after cancelled migrate: want *BootGateError, got %v", err)
	}
}

// TestMigrate_LadderNeverWritesSentinel proves SM-R3 (CRITICAL): the ladder
// must never stamp meta.scrub_baseline_complete — the sentinel is written ONLY
// by store.Migrate (both modes) and fresh-DB DDL. Otherwise the boot gate
// would auto-open without the human acknowledgment --no-rescrub encodes.
func TestMigrate_LadderNeverWritesSentinel(t *testing.T) {
	t.Run("full ladder leaves meta empty and gate closed", func(t *testing.T) {
		conn := loadFixtureStore(t, "schema_v0.sql")
		if err := db.Migrate(conn); err != nil {
			t.Fatalf("Migrate ladder: %v", err)
		}
		var n int
		if err := conn.QueryRow(`SELECT COUNT(*) FROM meta`).Scan(&n); err != nil {
			t.Fatalf("count meta: %v", err)
		}
		if n != 0 {
			t.Errorf("ladder wrote %d meta rows; the sentinel is store.Migrate's job", n)
		}
		var bgErr *db.BootGateError
		if err := db.AssertBootReady(conn); !errors.As(err, &bgErr) {
			t.Errorf("boot gate after ladder-only: want *BootGateError, got %v", err)
		}

		// Completion: store.Migrate (either mode) stamps '1' and the gate opens.
		s := store.New(conn)
		if _, err := store.Migrate(t.Context(), s, store.MigrateOptions{Rescrub: false}); err != nil {
			t.Fatalf("store.Migrate: %v", err)
		}
		if err := db.AssertBootReady(conn); err != nil {
			t.Errorf("gate still closed after store.Migrate stamp: %v", err)
		}
	})

	t.Run("pre-inserted zero sentinel survives the ladder", func(t *testing.T) {
		conn := loadFixtureStore(t, "schema_v0.sql")
		// v0 has no meta table yet; create it the way rung 0→1 will (IF NOT
		// EXISTS keeps the later rung a no-op) and pre-insert the sentinel as
		// a real pre-baseline DB would.
		if _, err := conn.Exec(`
			CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
			INSERT INTO meta(key, value) VALUES('scrub_baseline_complete', '0');
		`); err != nil {
			t.Fatalf("pre-insert sentinel: %v", err)
		}
		if err := db.Migrate(conn); err != nil {
			t.Fatalf("Migrate ladder: %v", err)
		}
		var v string
		if err := conn.QueryRow(
			`SELECT value FROM meta WHERE key = 'scrub_baseline_complete'`,
		).Scan(&v); err != nil {
			t.Fatalf("read sentinel after ladder: %v", err)
		}
		if v != "0" {
			t.Errorf("sentinel after ladder = %q, want '0' (rungs never touch it)", v)
		}

		s := store.New(conn)
		if _, err := store.Migrate(t.Context(), s, store.MigrateOptions{Rescrub: false}); err != nil {
			t.Fatalf("store.Migrate: %v", err)
		}
		if err := db.AssertBootReady(conn); err != nil {
			t.Errorf("gate still closed after store.Migrate stamp: %v", err)
		}
	})
}
