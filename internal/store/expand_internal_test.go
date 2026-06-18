package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/SamuelMolero26/droids-mem/internal/db"
	_ "modernc.org/sqlite"
)

// recordExpansion is best-effort: a failing UPDATE (here, a closed DB) must be
// swallowed, never panic, never propagate. In-package test so it can reach the
// unexported method directly.
func TestRecordExpansion_SwallowsError(t *testing.T) {
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Init(conn); err != nil {
		t.Fatalf("init: %v", err)
	}
	s := New(conn)

	// Close the DB out from under the recorder to force an Exec error.
	if err := conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Must not panic or block; returns nothing regardless of the failure.
	s.recordExpansion(context.Background(), "mem_anything")
}
