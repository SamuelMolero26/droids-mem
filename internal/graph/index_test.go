package graph

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestDedupeVariants_NoDuplicateQNameWithTestsEnabled pins the packages.Load
// duplication defect from Tests:true: packages.Load returns both a plain
// variant and an in-package test variant for a tested package, and the
// in-package test variant's p.Syntax re-parses the SAME production files
// under the SAME PkgPath as the plain variant. Without dedupe, every
// production symbol in that package gets emitted twice — symbols.qname has
// no UNIQUE constraint, so the duplicate insert silently succeeds — and
// findSymbol then reports every symbol in the package as ambiguous.
func TestDedupeVariants_NoDuplicateQNameWithTestsEnabled(t *testing.T) {
	repo := copyFixture(t)
	writeFile(t, filepath.Join(repo, "zz"), "zz_test.go", `package zz

import "testing"

func TestHubFromTest(t *testing.T) {
	Hub()
}
`)

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

	var dupes int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM (
		SELECT qname FROM symbols GROUP BY qname HAVING COUNT(*) > 1)`).Scan(&dupes); err != nil {
		t.Fatal(err)
	}
	if dupes != 0 {
		t.Errorf("%d duplicate qname group(s) in symbols table, want 0", dupes)
	}

	var n int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM symbols WHERE qname = 'zz.Hub'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("zz.Hub appears %d time(s) in symbols, want exactly 1 (findSymbol would report it ambiguous)", n)
	}
}

// TestDedupeVariants_FindSymbolResolvesUnambiguously is the end-to-end version
// of the same guarantee through the public query surface: a symbol in a
// tested package must resolve to exactly one match, not trip the "ambiguous"
// branch (len(rows) > 1) that duplicate qname rows would cause.
func TestDedupeVariants_FindSymbolResolvesUnambiguously(t *testing.T) {
	repo := copyFixture(t)
	writeFile(t, filepath.Join(repo, "zz"), "zz_test.go", `package zz

import "testing"

func TestHubFromTest(t *testing.T) {
	Hub()
}
`)

	m := NewManager(filepath.Join(t.TempDir(), "graphs"))
	t.Cleanup(m.Close)
	resp, err := m.Symbol(context.Background(), SymbolRequest{Repo: repo, Symbol: "zz.Hub"})
	if err != nil {
		t.Fatalf("Symbol: %v", err)
	}
	if resp.Symbol == nil {
		t.Fatalf("zz.Hub did not resolve to a single symbol, got matches=%+v hint=%q", resp.Matches, resp.Hint)
	}
	if resp.Symbol.QName != "zz.Hub" {
		t.Errorf("resolved symbol = %q, want zz.Hub", resp.Symbol.QName)
	}
}
