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
		db.Close()
		return nil, err
	}
	return db, nil
}

// BuildDSN returns a modernc.org/sqlite DSN with the canonical pragma set.
// Exposed so tests and the doctor command can open the same DB consistently.
func BuildDSN(path string) string {
	return "file:" + path + "?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(ON)"
}

// DDL -> creates all tables, indexes, and triggers
func Init(db *sql.DB) error {
	if _, err := db.Exec(ddl); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	return nil
}

func ResolvePath() string {
	if p := os.Getenv("DROIDS_MEM_DB"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".droids-mem", "mem.db")
}
