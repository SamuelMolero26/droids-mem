package graph

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/tools/go/packages"

	_ "modernc.org/sqlite"
)

// TestBuildIndex_MapperSymbols_LandInGraphDB is task C.1: buildIndex over a
// pure TS/Python fixture repo (no Go at all) must yield symbol rows in
// graph.db, with distinct non-zero IDs — the first PR where mapper output
// becomes observable at all. Per the PR-C constraint, this repo has no .go
// file, so testdata/testmod (which MUST stay Go-only) is never touched.
func TestBuildIndex_MapperSymbols_LandInGraphDB(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "app.ts", "export function hello(): string { return 'hi'; }\n")
	writeFile(t, repo, "util.py", "def greet():\n    pass\n")

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

	rows, err := conn.Query(`SELECT id, name FROM symbols ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	var names []string
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	if len(ids) < 2 {
		t.Fatalf("want at least 2 mapper symbol rows (hello, greet), got %d: %v", len(ids), names)
	}
	seen := map[int64]bool{}
	for _, id := range ids {
		if id == 0 {
			t.Errorf("symbol id 0 present, names=%v", names)
		}
		if seen[id] {
			t.Errorf("duplicate symbol id %d", id)
		}
		seen[id] = true
	}
	if !slices.Contains(names, "hello") || !slices.Contains(names, "greet") {
		t.Errorf("expected symbols hello and greet, got %v", names)
	}
}

// TestBuildIndex_MapperQNameCollisionCounted is tasks C.4/C.5 (T4 part 1),
// end-to-end: a/b/__init__.py and a/b.py both synthesize modulePath "a.b"
// (mapper.go's modulePath), so a same-named top-level def in both collides
// on qname. The collision must be COUNTED, not silently last-wins with no
// trace — visibility only, no remap logic in this PR (PR-E consumes it).
func TestBuildIndex_MapperQNameCollisionCounted(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "a", "b"), "__init__.py", "def foo():\n    pass\n")
	writeFile(t, repo, "a/b.py", "def foo():\n    pass\n")

	dbPath := filepath.Join(t.TempDir(), "graph.db")
	st, err := stamp(repo)
	if err != nil {
		t.Fatal(err)
	}

	before := mapperQNameCollisions.Load()
	if err := buildIndex(context.Background(), repo, dbPath, st); err != nil {
		t.Fatalf("buildIndex: %v", err)
	}
	if got := mapperQNameCollisions.Load() - before; got != 1 {
		t.Errorf("mapperQNameCollisions increased by %d, want 1 (a/b/__init__.py and a/b.py both module %q)", got, "a.b")
	}
}

// TestGoSymbols_ByPos_ExcludesMapperSymbols is task C.7: byPos (Go's
// SSA-function-matching map) must contain zero mapper-tier symbols, pinned
// directly rather than inferred from other tests. mapperSymbols has no byPos
// parameter at all, so this holds by construction — this test guards that
// invariant against ever being broken by a future refactor that threads one
// in.
func TestGoSymbols_ByPos_ExcludesMapperSymbols(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module x\n\ngo 1.21\n")
	writeFile(t, repo, "main.go", "package main\n\nfunc Hi() {}\n")

	rawPkgs, loadErr := packages.Load(goFreeLoadConfig(repo), "./...")
	pkgs := usableGoPackages(rawPkgs, loadErr)
	if len(pkgs) == 0 {
		t.Fatal("test setup: no Go packages loaded")
	}
	module := ""
	if pkgs[0].Module != nil {
		module = pkgs[0].Module.Path
	}
	readFile := func(name string) []byte {
		b, _ := os.ReadFile(name)
		return b
	}
	_, byPos := goSymbols(pkgs, module, pkgs[0].Fset, readFile, repo)
	if len(byPos) == 0 {
		t.Fatal("test setup: byPos is empty")
	}

	writeFile(t, repo, "app.ts", "export function hello() {}\n")
	mFiles, _, err := mapperFiles(repo)
	if err != nil {
		t.Fatal(err)
	}
	mSyms, _ := mapperSymbols(mFiles)
	if len(mSyms) == 0 {
		t.Fatal("test setup: no mapper symbols produced")
	}

	for _, ms := range mSyms {
		for pos, row := range byPos {
			if row == ms.row {
				t.Errorf("mapper symbol %q found in byPos at key %q — mapper rows must never enter Go's SSA-matching map", ms.row.qname, pos)
			}
		}
	}
}

// TestManager_IndexerGenBump_RebuildsPriorGraphWithMapperSymbols is task C.6
// (T1/D8), per PR-B's B.9 pattern extended to a full rebuild: a graph.db
// seeded exactly as a PR-B-era build would have left it (old indexerGen,
// matching census, zero symbols) must be treated as stale by the CURRENT
// stamp — forcing a rebuild that now returns mapper symbols a PR-B-era build
// could never have produced.
func TestManager_IndexerGenBump_RebuildsPriorGraphWithMapperSymbols(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "app.ts", "export function hello(): string { return 'hi'; }\n")

	m := managerFor(t)
	ctx := context.Background()

	canon, err := canonicalRepo(repo)
	if err != nil {
		t.Fatal(err)
	}
	dbPath := m.dbPath(canon)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o750); err != nil {
		t.Fatal(err)
	}

	st, err := stamp(repo)
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(st, ":")
	if !ok {
		t.Fatalf("stamp() = %q, want a %q-separated generation prefix", st, ":")
	}
	// Same census (count/size/maxMtime) as the CURRENT tree, but the PR-B-era
	// generation — so a mismatch can only be attributed to indexerGen, not an
	// unrelated census difference.
	oldStamp := stampGen(schema, indexedExtensions(), "1") + ":" + rest
	seedRawGraphMeta(t, dbPath, oldStamp, nil)

	resp, err := m.WaitBuild(ctx, repo, 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Completed || !resp.Rebuilt {
		t.Fatalf("want completed+rebuilt (indexerGen bump must force a rebuild of a PR-B-era graph), got %+v", resp)
	}

	symResp, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "hello"})
	if err != nil {
		t.Fatalf("Symbol hello: %v", err)
	}
	if symResp.Symbol == nil {
		t.Fatal("Symbol hello: nil symbol in response — the rebuild did not produce mapper symbols")
	}
}

// TestBuildIndex_BothTiersEmpty_HardErrors is task C.10: reinstated now that
// mapper discovery exists to make the check meaningful. PR-B deliberately
// left the Go-free path always non-erroring, because mapper discovery wasn't
// wired in yet — see PR-B apply-progress "B.4 Deviation". A repo with neither
// a usable Go package nor a single mapper-tier symbol has nothing to build
// from at all.
func TestBuildIndex_BothTiersEmpty_HardErrors(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "README.md", "# nothing indexable here\n")

	dbPath := filepath.Join(t.TempDir(), "graph.db")
	st, err := stamp(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := buildIndex(context.Background(), repo, dbPath, st); err == nil {
		t.Fatal("buildIndex succeeded on a repo with neither Go packages nor mapper symbols, want a hard error")
	}
}

// TestBuildIndex_GoPackageWithZeroDecls_EmptyReasonHint replaces PR-B's
// TestBuildIndex_GoFreeTree_EmptyReasonHint: that test's scenario (a Go-free
// tree with a single .ts file) was explicitly scoped by the spec to "the
// PR-B-to-PR-C window" — once mapper discovery is wired (C.2), that exact
// fixture now produces a mapper symbol and is no longer empty at all. The
// residual "successful build, zero symbols" case post-C.10 is a real Go
// package that type-checks but declares nothing at the top level: the Go
// tier "ran" (len(pkgs) > 0), so the both-tiers-empty hard error does not
// apply, and the build must still report meta.empty_reason honestly.
func TestBuildIndex_GoPackageWithZeroDecls_EmptyReasonHint(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module x\n\ngo 1.21\n")
	writeFile(t, repo, "main.go", "package main\n")

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

	var reason string
	if err := conn.QueryRow(`SELECT value FROM meta WHERE key = 'empty_reason'`).Scan(&reason); err != nil {
		t.Fatalf("read meta.empty_reason: %v", err)
	}
	if reason != "no_indexable_symbols" {
		t.Errorf("meta.empty_reason = %q, want %q", reason, "no_indexable_symbols")
	}
}

// TestBuildIndex_MajorityBrokenGo_MapperSymbolsStillLand is task C.11: a
// mixed repo whose Go half is majority-broken but whose mapper-tier files
// parse fine must still yield mapper symbol rows — the majority-broken cap
// must suppress only the Go tier, not abort the whole build.
func TestBuildIndex_MajorityBrokenGo_MapperSymbolsStillLand(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module x\n\ngo 1.21\n")
	writeFile(t, repo, "a.go", "package x\n\nfunc A() { undefined( }\n")
	writeFile(t, repo, "b.go", "package x\n\nfunc B() { undefined( }\n")
	writeFile(t, repo, "app.ts", "export function hello(): string { return 'hi'; }\n")

	dbPath := filepath.Join(t.TempDir(), "graph.db")
	st, err := stamp(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := buildIndex(context.Background(), repo, dbPath, st); err != nil {
		t.Fatalf("buildIndex must suppress the Go tier and still succeed via mapper symbols, got: %v", err)
	}

	conn, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	var n int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM symbols WHERE name = 'hello'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("mapper symbol %q count = %d, want 1", "hello", n)
	}

	var reason string
	if err := conn.QueryRow(`SELECT value FROM meta WHERE key = 'empty_reason'`).Scan(&reason); err != nil {
		t.Fatalf("read meta.empty_reason: %v", err)
	}
	if reason != "go_tier_suppressed_majority_broken" {
		t.Errorf("meta.empty_reason = %q, want %q (suppression must be disclosed, not silent)", reason, "go_tier_suppressed_majority_broken")
	}
}

// TestBuildIndex_MajorityBrokenGo_NoMapperContent_StillHardErrors pins that
// C.11 does not weaken the majority-broken cap's existing protection for a
// Go-only repo: with no mapper content available to compensate, the old
// "serving previous graph" hard error must still fire exactly as before.
func TestBuildIndex_MajorityBrokenGo_NoMapperContent_StillHardErrors(t *testing.T) {
	repo := copyFixture(t)
	breakMajority(t, repo)

	dbPath := filepath.Join(t.TempDir(), "graph.db")
	st, err := stamp(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := buildIndex(context.Background(), repo, dbPath, st); err == nil {
		t.Fatal("buildIndex succeeded on a majority-broken Go-only repo, want the existing hard error preserved")
	}
}
