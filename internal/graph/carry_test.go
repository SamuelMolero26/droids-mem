package graph

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// seedPrevGraph writes a minimal, real graph.db at path with the given
// symbols (qname, package) and edges (caller qname, callee qname) — a
// hand-seeded fixture standing in for "a previous build's graph.db",
// carriedEdges' only input.
func seedPrevGraph(t *testing.T, path string, syms []struct{ qname, pkg string }, edges [][2]string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for i, s := range syms {
		id := int64(i + 1)
		ids[s.qname] = id
		if _, err := db.Exec(`INSERT INTO symbols
			(id, qname, name, kind, package, file, line, exported, signature) VALUES (?,?,?,?,?,?,?,?,?)`,
			id, s.qname, s.qname, "func", s.pkg, "f.go", 1, 1, ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range edges {
		callerID, ok := ids[e[0]]
		if !ok {
			t.Fatalf("seed edge caller %q not in syms", e[0])
		}
		calleeID, ok := ids[e[1]]
		if !ok {
			t.Fatalf("seed edge callee %q not in syms", e[1])
		}
		if _, err := db.Exec(`INSERT INTO edges (caller, callee) VALUES (?,?)`, callerID, calleeID); err != nil {
			t.Fatal(err)
		}
	}
}

// TestCarriedEdges_CallerInBrokenPackageIsCarriedAndRemapped pins task 4.1/4.2
// (spec "Caller-in-broken-package edge is carried"): an edge whose caller
// qname belongs to a broken package must appear in the result with both
// endpoints remapped from the seeded (old) id to the fresh (new) id supplied
// via byQName.
func TestCarriedEdges_CallerInBrokenPackageIsCarriedAndRemapped(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	seedPrevGraph(t, dbPath,
		[]struct{ qname, pkg string }{
			{"zz.Near", "zz"}, {"zz.Hub", "zz"},
		},
		[][2]string{{"zz.Near", "zz.Hub"}},
	)

	broken := map[string]bool{"zz": true}
	byQName := map[string]int64{"zz.Near": 501, "zz.Hub": 502} // fresh ids, deliberately different from the seeded 1/2

	got := carriedEdges(dbPath, broken, byQName)
	if len(got) != 1 {
		t.Fatalf("got %d edges, want 1: %v", len(got), got)
	}
	if dispatch, ok := got[[2]int64{501, 502}]; !ok {
		t.Errorf("edge not remapped to the fresh ids: %v", got)
	} else if dispatch != "static" {
		// seedPrevGraph does not set a per-edge dispatch, so it falls back to
		// the schema's DEFAULT 'static' — carriedEdges must preserve whatever
		// value the previous graph.db actually stored (see
		// TestCarriedEdges_PreservesRealDispatchLabel for the non-default case).
		t.Errorf("carried edge dispatch = %q, want %q", dispatch, "static")
	}
}

// TestCarriedEdges_CallerQNameMissDropsEdge pins spec "Deleted symbol drops
// its carried edge": a previous edge whose caller qname no longer exists in
// the fresh symbol set must not be carried, not carried with a stale id.
func TestCarriedEdges_CallerQNameMissDropsEdge(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	seedPrevGraph(t, dbPath,
		[]struct{ qname, pkg string }{
			{"zz.Deleted", "zz"}, {"zz.Hub", "zz"},
		},
		[][2]string{{"zz.Deleted", "zz.Hub"}},
	)

	broken := map[string]bool{"zz": true}
	byQName := map[string]int64{"zz.Hub": 502} // zz.Deleted no longer exists in the fresh symbol set

	got := carriedEdges(dbPath, broken, byQName)
	if len(got) != 0 {
		t.Errorf("want 0 edges when the caller qname has no fresh match, got %v", got)
	}
}

// TestCarriedEdges_CrossUnitEdgeNeverCarried pins spec "Cross-unit edges are
// never carried": an edge whose caller is in a CLEAN package and whose
// callee is in a broken package must never be carried, even though it
// existed in the previous build.
func TestCarriedEdges_CrossUnitEdgeNeverCarried(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	seedPrevGraph(t, dbPath,
		[]struct{ qname, pkg string }{
			{"testmod.main", "testmod"}, {"zz.Hub", "zz"},
		},
		[][2]string{{"testmod.main", "zz.Hub"}}, // clean caller -> broken callee
	)

	broken := map[string]bool{"zz": true} // testmod is clean, zz is broken
	byQName := map[string]int64{"testmod.main": 601, "zz.Hub": 602}

	got := carriedEdges(dbPath, broken, byQName)
	if len(got) != 0 {
		t.Errorf("a clean-caller-into-broken-callee edge must never be carried, got %v", got)
	}
}

// seedPrevGraphNoDispatchColumn writes a graph.db using the PRE-5.1 edges
// schema (no dispatch column) — the only shape that can genuinely appear on
// disk after this PR ships: a graph.db written by a PR1/PR2-era build.
// Pins task 5.6 (Boundary Verifications §3): carriedEdges' SELECT now
// includes e.dispatch (5.7), so reading an old-schema graph.db must fail
// with "no such column: dispatch" and be swallowed by the strictly
// best-effort contract already established in 4.2 — zero carried edges, no
// error propagated to buildIndex.
func seedPrevGraphNoDispatchColumn(t *testing.T, path string, syms []struct{ qname, pkg string }, edges [][2]string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const oldSchema = `
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE symbols (
  id INTEGER PRIMARY KEY, qname TEXT NOT NULL, name TEXT NOT NULL, kind TEXT NOT NULL,
  package TEXT NOT NULL, file TEXT NOT NULL, line INTEGER NOT NULL, exported INTEGER NOT NULL,
  signature TEXT NOT NULL, doc TEXT NOT NULL DEFAULT '', source TEXT NOT NULL DEFAULT ''
);
CREATE TABLE edges (caller INTEGER NOT NULL, callee INTEGER NOT NULL, PRIMARY KEY (caller, callee)) WITHOUT ROWID;
`
	if _, err := db.Exec(oldSchema); err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for i, s := range syms {
		id := int64(i + 1)
		ids[s.qname] = id
		if _, err := db.Exec(`INSERT INTO symbols
			(id, qname, name, kind, package, file, line, exported, signature) VALUES (?,?,?,?,?,?,?,?,?)`,
			id, s.qname, s.qname, "func", s.pkg, "f.go", 1, 1, ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range edges {
		callerID, ok := ids[e[0]]
		if !ok {
			t.Fatalf("seed edge caller %q not in syms", e[0])
		}
		calleeID, ok := ids[e[1]]
		if !ok {
			t.Fatalf("seed edge callee %q not in syms", e[1])
		}
		if _, err := db.Exec(`INSERT INTO edges (caller, callee) VALUES (?,?)`, callerID, calleeID); err != nil {
			t.Fatal(err)
		}
	}
}

// TestCarriedEdges_OldSchemaPrevGraphYieldsZeroCarried pins task 5.6 (spec
// "Previous graph.db predates the dispatch column"): reading dispatch from a
// PR1/PR2-era graph.db must fail with a schema error, and carriedEdges must
// still return zero edges and no error — never propagated as a buildIndex
// failure.
func TestCarriedEdges_OldSchemaPrevGraphYieldsZeroCarried(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	seedPrevGraphNoDispatchColumn(t, dbPath,
		[]struct{ qname, pkg string }{
			{"zz.Near", "zz"}, {"zz.Hub", "zz"},
		},
		[][2]string{{"zz.Near", "zz.Hub"}},
	)

	broken := map[string]bool{"zz": true}
	byQName := map[string]int64{"zz.Near": 501, "zz.Hub": 502}

	got := carriedEdges(dbPath, broken, byQName)
	if len(got) != 0 {
		t.Errorf("want 0 carried edges reading dispatch from a pre-dispatch-column graph.db, got %v", got)
	}
}

// TestCarriedEdges_PreservesRealDispatchLabel pins task 5.7 (GREEN half of
// Boundary Verifications §3): once the dispatch column exists, carriedEdges
// must SELECT and preserve the real per-edge label from the previous
// graph.db instead of defaulting every carried edge to "static".
func TestCarriedEdges_PreservesRealDispatchLabel(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO symbols (id,qname,name,kind,package,file,line,exported,signature)
		VALUES (1,'zz.Near','Near','func','zz','f.go',1,1,'')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO symbols (id,qname,name,kind,package,file,line,exported,signature)
		VALUES (2,'zz.Hub','Hub','func','zz','f.go',1,1,'')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO edges (caller,callee,dispatch) VALUES (1,2,'interface')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()

	broken := map[string]bool{"zz": true}
	byQName := map[string]int64{"zz.Near": 501, "zz.Hub": 502}

	got := carriedEdges(dbPath, broken, byQName)
	dispatch, ok := got[[2]int64{501, 502}]
	if !ok {
		t.Fatalf("edge not carried: %v", got)
	}
	if dispatch != "interface" {
		t.Errorf("carried edge dispatch = %q, want the preserved real label %q", dispatch, "interface")
	}
}

// TestCarriedEdges_NoPreviousGraphYieldsZeroCarried pins spec "No previous
// graph exists": carry-forward is strictly best-effort, so a missing prev
// graph.db (first-ever build on a broken tree) must not be an error — zero
// edges carried, build still succeeds.
func TestCarriedEdges_NoPreviousGraphYieldsZeroCarried(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "does-not-exist.db")
	broken := map[string]bool{"zz": true}
	byQName := map[string]int64{"zz.Hub": 1}

	got := carriedEdges(dbPath, broken, byQName)
	if len(got) != 0 {
		t.Errorf("want 0 carried edges with no previous graph, got %v", got)
	}
}
