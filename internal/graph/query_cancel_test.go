package graph

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// Symbol and Package accept a context and use it for staleness and rebuilds,
// but every helper below them took a bare *sql.DB, so the context stopped at
// the door and no SQL was cancellable. That matters most on the deep walks:
// bfsNeighbors issues one query per BFS level with an IN-list that grows with
// the frontier, which is the slowest thing the graph does. Once it started,
// an expired MCP deadline could not stop it.
//
// Table-driven over every helper that reaches SQL, so a future helper added
// without a context is caught here rather than in production.
func TestGraphQueries_RespectContextCancellation(t *testing.T) {
	repo, err := filepath.Abs("testdata/testmod")
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	st, err := stamp(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := buildIndex(context.Background(), repo, dbPath, st); err != nil {
		t.Fatalf("buildIndex: %v", err)
	}

	conn, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	var id int64
	var qname, pkg string
	if err := conn.QueryRow(
		`SELECT id, qname, package FROM symbols WHERE kind='func' LIMIT 1`).Scan(&id, &qname, &pkg); err != nil {
		t.Fatalf("seed symbol: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already dead before any helper runs

	tests := []struct {
		name string
		call func() error
	}{
		{"edgeCount", func() error { _, err := edgeCount(ctx, conn, "caller", id); return err }},
		{"findSymbol", func() error { _, err := findSymbol(ctx, conn, qname); return err }},
		{"searchSymbols", func() error { _, err := searchSymbols(ctx, conn, "announce greeting"); return err }},
		{"transitiveCallers", func() error { _, err := transitiveCallers(ctx, conn, id); return err }},
		{"typeHasMethods", func() error { _, err := typeHasMethods(ctx, conn, qname); return err }},
		{"implementers", func() error { _, _, _, err := implementers(ctx, conn, id); return err }},
		{"satisfies", func() error { _, _, err := satisfies(ctx, conn, id); return err }},
		{"bfsNeighbors", func() error { _, _, err := bfsNeighbors(ctx, conn, id, "up", 3, pkg); return err }},
		{"callPath", func() error { _, err := callPath(ctx, conn, id, qname); return err }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			if !errors.Is(err, context.Canceled) {
				t.Errorf("%s returned %v, want context.Canceled — the context is not reaching its SQL", tt.name, err)
			}
		})
	}
}

// The public entry points must surface cancellation too, not swallow it into a
// not-found or an empty response.
func TestSymbol_CancelledContextIsAnError(t *testing.T) {
	m, repo := testManager(t)
	if _, err := m.Index(context.Background(), repo); err != nil {
		t.Fatalf("Index: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "Announce"}); err == nil {
		t.Error("Symbol with a cancelled context returned no error")
	}
}
