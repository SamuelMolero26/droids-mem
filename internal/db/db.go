package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
)

import _ "modernc.org/sqlite"

func Open() (*sql.DB, error) {
	path := ResolvePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	// PRAGMAs in DSN apply to every connection the pool opens. The legacy
	// applyPragmas() path runs them on a single checked-out conn, leaving
	// siblings in the pool with default settings — that path is unsafe.
	db, err := sql.Open("sqlite", BuildDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// Single-writer SQLite: cap pool at 1 to guarantee BEGIN IMMEDIATE in
	// save.go holds the only writer conn. Without this, the pool may open
	// sibling conns that bypass the dedicated-conn lock semantics.
	// Future.md tracks the dual-pool (writer + read-only readers) option.
	db.SetMaxOpenConns(1)

	if err := Init(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	// SQLite creates the DB (and WAL/SHM) with umask-default perms, typically
	// 0644. The 0700 state dir shields them today, but the files themselves
	// hold unencrypted memory content — tighten to owner-only as defense in
	// depth. Also repairs files created by older builds. WAL/SHM exist by now
	// because Init just wrote schema/migrations.
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(p, 0o600); err != nil && !os.IsNotExist(err) {
			_ = db.Close()
			return nil, fmt.Errorf("tighten db perms on %s: %w", p, err)
		}
	}
	return db, nil
}

// BuildDSN returns a modernc.org/sqlite DSN with the canonical pragma set.
// Exposed so tests and the doctor command can open the same DB consistently.
func BuildDSN(path string) string {
	// cache_size=-4000 → 4 MB page cache (default is -2000 = 2 MB).
	// Modest bump for the read-heavy context/search paths. Negative value
	// means "KiB", positive would mean "pages". mmap_size deliberately
	// omitted — measured benefit on a short-lived CLI is unproven and
	// macOS + mmap on growing files has known sharp edges. Tracked in
	// Future.md.
	return "file:" + path + "?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=cache_size(-4000)"
}

// Init prepares db for use. For a fresh database (no memories table) it runs
// the full v1.0 DDL and stamps PRAGMA user_version = CurrentSchemaVersion.
// For an existing database it advances the schema via the migration ladder.
// Init is idempotent: a second call on an already-current DB is a no-op.
func Init(db *sql.DB) error {
	fresh, err := isFreshDB(db)
	if err != nil {
		return err
	}
	if fresh {
		if _, err := db.Exec(ddl); err != nil {
			return fmt.Errorf("init schema: %w", err)
		}
		if err := setUserVersion(db, CurrentSchemaVersion); err != nil {
			return err
		}
		return nil
	}
	return Migrate(db)
}

// isFreshDB returns true when the memories table is absent — the marker that
// no prior droids-mem schema has been installed. We check the table rather
// than PRAGMA user_version because pre-v1.0 databases were created without
// stamping a version (so v0 and "brand new" both report user_version = 0).
func isFreshDB(db *sql.DB) (bool, error) {
	var count int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='memories'`,
	).Scan(&count); err != nil {
		return false, fmt.Errorf("inspect schema: %w", err)
	}
	return count == 0, nil
}

func ResolvePath() string {
	if p := os.Getenv("DROIDS_MEM_DB"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".droids-mem", "mem.db")
}
