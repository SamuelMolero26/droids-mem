package graph

import (
	"context"
	"database/sql"
	"go/token"
	"go/types"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"

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

// TestAssertImportClosure_Holds pins the direct-call contract for the
// reachable-import-closure precondition (task 3.1/3.2, spec "Closure holds"):
// when every package reachable via import from a created package itself has
// a corresponding ssa.Package, the check returns nil and never calls
// prog.Build().
func TestAssertImportClosure_Holds(t *testing.T) {
	fset := token.NewFileSet()
	typesB := types.NewPackage("example.com/b", "b")
	typesB.MarkComplete()
	typesA := types.NewPackage("example.com/a", "a")
	typesA.MarkComplete()

	pkgB := &packages.Package{PkgPath: "example.com/b", Types: typesB}
	pkgA := &packages.Package{
		PkgPath: "example.com/a",
		Types:   typesA,
		Imports: map[string]*packages.Package{"example.com/b": pkgB},
	}

	prog := ssa.NewProgram(fset, 0)
	prog.CreatePackage(typesA, nil, nil, true)
	prog.CreatePackage(typesB, nil, nil, true)

	if err := assertImportClosure(prog, []*packages.Package{pkgA, pkgB}); err != nil {
		t.Errorf("closure holds but assertImportClosure returned an error: %v", err)
	}
}

// TestAssertImportClosure_Violated pins the other half: a created-package set
// that omits a package reachable via import from an included package must
// produce a non-nil error, and this test asserts that WITHOUT ever calling
// prog.Build() on the incomplete set — prog.Build() panics from goroutines it
// spawns when this precondition doesn't hold, and no test may reach that
// panic (spec "No SSA panic is ever asserted by a test").
func TestAssertImportClosure_Violated(t *testing.T) {
	fset := token.NewFileSet()
	typesB := types.NewPackage("example.com/b", "b")
	typesB.MarkComplete()
	typesA := types.NewPackage("example.com/a", "a")
	typesA.MarkComplete()

	pkgB := &packages.Package{PkgPath: "example.com/b", Types: typesB}
	pkgA := &packages.Package{
		PkgPath: "example.com/a",
		Types:   typesA,
		Imports: map[string]*packages.Package{"example.com/b": pkgB},
	}

	prog := ssa.NewProgram(fset, 0)
	prog.CreatePackage(typesA, nil, nil, true) // typesB is deliberately never created

	err := assertImportClosure(prog, []*packages.Package{pkgA})
	if err == nil {
		t.Fatal("want a non-nil error: pkgA imports example.com/b, which has no ssa.Package")
	}
	// The whole point: prog.Build() is never reached on this incomplete set.
	// If it were called here, the test binary would crash from a panic no
	// recover() can catch — that is the failure mode this test exists to rule
	// out by construction (assertImportClosure is called directly, not via
	// buildIndex/callEdges).
}

// TestBuildIndex_BrokenPackageStillYieldsSymbols pins the partition
// requirement (task 3.3/3.4, spec "Broken package still yields symbols"):
// a type error confined to one package's function body must not abort the
// whole build — symbol rows for every declaration in the broken package must
// still appear, sourced from AST alone. Uses the existing copyFixture +
// writeFile break-injection harness from build_test.go, no new machinery.
func TestBuildIndex_BrokenPackageStillYieldsSymbols(t *testing.T) {
	repo := copyFixture(t)
	writeFile(t, filepath.Join(repo, "zz"), "zz_broken.go", `package zz

func Broken() {
	var x int = "this does not type-check"
	_ = x
}
`)

	dbPath := filepath.Join(t.TempDir(), "graph.db")
	st, err := stamp(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := buildIndex(context.Background(), repo, dbPath, st); err != nil {
		t.Fatalf("buildIndex must partition around a single body-local type error, got: %v", err)
	}

	conn, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	for _, qname := range []string{"zz.Hub", "zz.Near", "zz.Broken"} {
		var n int
		if err := conn.QueryRow(`SELECT COUNT(*) FROM symbols WHERE qname = ?`, qname).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("symbol %q: got %d rows in the broken package, want 1 (AST-sourced regardless of type errors)", qname, n)
		}
	}
}

// TestBuildIndex_BrokenPackageInEdgesSurvive pins task 3.5/3.6 (spec "Broken
// package in-edges survive"): a broken package called by several clean
// packages must keep its in-edges in the collected edge set. Before the
// callEdges rewrite (packages.Visit + CreatePackage stubs, no
// DeleteSyntheticNodes), ssautil.AllPackages transitively filters out
// IllTyped packages — the broken package and everything CHA would otherwise
// resolve into it — silently dropping these in-edges.
func TestBuildIndex_BrokenPackageInEdgesSurvive(t *testing.T) {
	repo := copyFixture(t)
	// zz.Hub is called by testmod.main (cross-package, clean caller) and
	// zz.Near (same-package caller). Adding a body-local type error to zz
	// breaks the WHOLE zz package under go/packages, while main.go stays
	// clean — exactly the "broken package called by clean packages" shape.
	writeFile(t, filepath.Join(repo, "zz"), "zz_broken.go", `package zz

func Broken() {
	var x int = "this does not type-check"
	_ = x
}
`)

	dbPath := filepath.Join(t.TempDir(), "graph.db")
	st, err := stamp(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := buildIndex(context.Background(), repo, dbPath, st); err != nil {
		t.Fatalf("buildIndex must partition around a single body-local type error, got: %v", err)
	}

	conn, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// testmod.main is a CLEAN package (full SSA body) calling into the broken
	// zz package's types-only stub — this is exactly the scenario
	// DeleteSyntheticNodes used to destroy (it deletes every node with no
	// syntax, including every function in the stub, so splicing in->out
	// through a deleted node drops the in-edge too).
	//
	// zz.Near -> zz.Hub (a caller INSIDE the broken package) is deliberately
	// NOT asserted here: zz.Near has no SSA body either (same stub, no
	// syntax), so that edge cannot be freshly discovered by callEdges at
	// all — it can only be recovered via carry-forward from a previous
	// graph.db (Phase 4), which this test does not have.
	var n int
	err = conn.QueryRow(`SELECT COUNT(*) FROM edges e
		JOIN symbols s1 ON s1.id = e.caller
		JOIN symbols s2 ON s2.id = e.callee
		WHERE s1.qname = 'testmod.main' AND s2.qname = 'zz.Hub'`).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("edge testmod.main -> zz.Hub: got %d, want 1 (in-edge from a clean caller into a broken package's stub must survive)", n)
	}
}

// TestBuildIndex_CarryForwardRecoversBrokenPackageInternalEdge is the
// end-to-end proof of task 4.3: buildIndex called twice against the SAME
// dbPath (exactly the production shape — a rebuild reads the still-in-place
// previous graph.db before writeGraphDB renames a fresh one over it).
//
// zz.Near -> zz.Hub is a caller-inside-the-broken-package edge:
// TestBuildIndex_BrokenPackageInEdgesSurvive already established that
// callEdges alone cannot freshly discover it once zz is broken (zz.Near has
// no SSA body in a types-only stub). Carry-forward is the ONLY mechanism
// that can recover it, and only because a first clean build already put it
// in dbPath.
func TestBuildIndex_CarryForwardRecoversBrokenPackageInternalEdge(t *testing.T) {
	repo := copyFixture(t)
	dbPath := filepath.Join(t.TempDir(), "graph.db")

	st1, err := stamp(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := buildIndex(context.Background(), repo, dbPath, st1); err != nil {
		t.Fatalf("first (clean) buildIndex: %v", err)
	}

	writeFile(t, filepath.Join(repo, "zz"), "zz_broken.go", `package zz

func Broken() {
	var x int = "this does not type-check"
	_ = x
}
`)
	st2, err := stamp(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := buildIndex(context.Background(), repo, dbPath, st2); err != nil {
		t.Fatalf("second (partial) buildIndex: %v", err)
	}

	conn, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	var n int
	err = conn.QueryRow(`SELECT COUNT(*) FROM edges e
		JOIN symbols s1 ON s1.id = e.caller
		JOIN symbols s2 ON s2.id = e.callee
		WHERE s1.qname = 'zz.Near' AND s2.qname = 'zz.Hub'`).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("edge zz.Near -> zz.Hub: got %d, want 1 (must be carried forward from the previous graph.db)", n)
	}
}
