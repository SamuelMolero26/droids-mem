package graph

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------- E.1: carry-trigger scenarios (pure, table) ----------

// TestMapperCarryTrigger pins task E.1's three carry-trigger scenarios
// (design D6, ADR-0034 decision 8): ERROR nodes alone never carry, a halved
// def count alone never carries, only their conjunction does.
func TestMapperCarryTrigger(t *testing.T) {
	cases := []struct {
		name                   string
		hasError               bool
		defCount, prevDefCount int
		want                   bool
	}{
		{"ERROR nodes without def-count halving does not carry", true, 4, 6, false}, // 4 !< 3
		{"ERROR nodes with halved def count carries", true, 2, 6, true},             // 2 < 3
		{"zero ERROR nodes never carries, even with a halved count", false, 1, 6, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mapperCarryTrigger(c.hasError, c.defCount, c.prevDefCount)
			if got != c.want {
				t.Errorf("mapperCarryTrigger(%v, %d, %d) = %v, want %v",
					c.hasError, c.defCount, c.prevDefCount, got, c.want)
			}
		})
	}
}

// ---------- E.2: two-generation runtime harness (symbols AND edges) ----------

// TestMapperCarry_TriggeredFile_CarriesSymbolsAndEdges is the runtime harness
// for E.1/E.2 (design D6): a TS file with 6 top-level functions is indexed
// cleanly, then corrupted down to a fragment that both contains an ERROR node
// and falls well under half its previous def count — the exact trigger. The
// rebuild must carry BOTH the missing symbols (fnC) and the fnA->fnB edge
// forward from the previous graph.db, not lose them outright.
func TestMapperCarry_TriggeredFile_CarriesSymbolsAndEdges(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "app.ts", `export function fnA() { fnB(); }
export function fnB() {}
export function fnC() {}
export function fnD() {}
export function fnE() {}
export function fnF() {}
`)

	m := managerFor(t)
	ctx := context.Background()
	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatalf("clean generation index: %v", err)
	}

	// Corrupt: truncate mid-edit, leaving an ERROR node and zero of the
	// previous 6 defs discoverable — well under half.
	writeFile(t, repo, "app.ts", "export function fnA() { fnB(\n")
	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatalf("corrupted generation index: %v", err)
	}

	// Symbol carried: fnC has no edges at all, so this isolates the
	// symbol-carry half from the edge-carry half.
	resp, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "fnC"})
	if err != nil {
		t.Fatalf("Symbol fnC: %v", err)
	}
	if resp.Symbol == nil {
		t.Fatal("fnC not found — carried symbols were lost, not carried forward")
	}
	if !resp.Carried {
		t.Errorf("fnC.Carried = false, want true (its file was carried forward)")
	}

	// Edge carried: fnA -> fnB must survive via fnB's callers, even though
	// fnA itself is also a carried (id==0 at build time) row.
	callersResp, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "fnB", Direction: "up"})
	if err != nil {
		t.Fatalf("Symbol fnB up: %v", err)
	}
	found := false
	for _, c := range callersResp.Callers {
		if strings.Contains(c.QName, "fnA") {
			found = true
		}
	}
	if !found {
		t.Errorf("fnB callers = %v, want fnA carried forward as a caller", callersResp.Callers)
	}
}

// TestMapperCarry_NoErrorNeverCarries guards the common case directly through
// the real build pipeline: a clean rebuild (no ERROR nodes at all) must never
// substitute carried rows, even when the def count genuinely shrinks (a
// legitimate deletion, not corruption).
func TestMapperCarry_NoErrorNeverCarries(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "app.ts", `export function fnA() {}
export function fnB() {}
export function fnC() {}
export function fnD() {}
`)

	m := managerFor(t)
	ctx := context.Background()
	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatalf("clean generation index: %v", err)
	}

	// A clean, deliberate deletion of most defs — no ERROR node anywhere.
	writeFile(t, repo, "app.ts", "export function fnA() {}\n")
	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatalf("second clean generation index: %v", err)
	}

	resp, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "fnD"})
	if err == nil && resp != nil && resp.Symbol != nil {
		t.Errorf("fnD must not be carried forward after a clean (error-free) deletion, got %+v", resp.Symbol)
	}
}

// ---------- E.3/E.4 (T3): carriedUnits keyed by modulePath ----------

// TestMapperCarry_CarriedUnitsKeyedByModulePath_FreshnessCarriedFires pins
// task E.3/E.4: a carried mapper file's entry in carriedUnits must be its
// MODULE PATH (symRow.pkg), not its rel path — query.go's per-symbol Carried
// check (:202) compares fresh.carriedUnits against info.Package, which for a
// mapper row IS the module path. Keying by rel would make Carried silently
// never fire for a carried mapper symbol.
func TestMapperCarry_CarriedUnitsKeyedByModulePath_FreshnessCarriedFires(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "a"), "b.py", "def foo():\n    pass\n\n\ndef bar():\n    pass\n\n\ndef baz():\n    pass\n")

	m := managerFor(t)
	ctx := context.Background()
	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatalf("clean generation index: %v", err)
	}

	// Corrupt: truncate mid-edit — ERROR node, zero of the previous 3 defs
	// discoverable.
	writeFile(t, filepath.Join(repo, "a"), "b.py", "def foo(\n")
	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatalf("corrupted generation index: %v", err)
	}

	resp, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "bar"})
	if err != nil {
		t.Fatalf("Symbol bar: %v", err)
	}
	if resp.Symbol == nil {
		t.Fatal("bar not found — carried symbols were lost")
	}
	if resp.Symbol.Package != "a.b" {
		t.Fatalf("test setup: bar.Package = %q, want the module path %q", resp.Symbol.Package, "a.b")
	}
	if !resp.Carried {
		t.Errorf("bar.Carried = false, want true — carriedUnits must be keyed by modulePath (%q), not rel (%q)",
			"a.b", filepath.ToSlash(filepath.Join("a", "b.py")))
	}
	if !strings.Contains(resp.Hint, carriedHint) {
		t.Errorf("carried mapper symbol must carry carriedHint, got hint %q", resp.Hint)
	}
}

// ---------- E.6/E.7 (T4 part 2): drop-on-qname-collision ----------

// TestMapperCarriedEdges_DropsOnCalleeQNameCollision pins task E.6/E.7: when
// a carried edge's TARGET (callee) qname collided at buildByQName time, the
// edge must be DROPPED, never remapped onto the possibly-wrong last-wins row
// collision produces. Uses the SAME genuine modulePath collision as PR-C's
// C.4/C.5 (a/b/__init__.py vs a/b.py, both -> module "a.b" via the real
// modulePath function), run through the real mapperFiles -> mapperSymbols ->
// buildByQName pipeline — not a re-derivation — so collidedQNames is proven
// non-empty for THIS fixture before the drop is even asserted. A distinct
// caller module (never itself part of the collision) is included so the
// caller side resolves cleanly in byQName — the ONLY reason this edge can be
// dropped is the collision check, not an unrelated byQName miss.
func TestMapperCarriedEdges_DropsOnCalleeQNameCollision(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "a", "b"), "__init__.py", "def foo():\n    pass\n")
	writeFile(t, repo, "a/b.py", "def foo():\n    pass\n")
	writeFile(t, repo, "caller.py", "def main():\n    pass\n")

	mFiles, _, err := mapperFiles(repo)
	if err != nil {
		t.Fatal(err)
	}
	mSyms, _ := mapperSymbols(mFiles)
	if len(mSyms) == 0 {
		t.Fatal("test setup: no mapper symbols produced")
	}

	symbols := make([]*symRow, 0, len(mSyms))
	for _, ms := range mSyms {
		symbols = append(symbols, ms.row)
	}
	for i, s := range symbols {
		s.id = int64(i + 1)
	}

	byQName, collided := buildByQName(symbols)

	// The set must actually be POPULATED for this fixture — a guard that
	// only exercises the drop against an always-empty set would pass
	// identically whether or not collision tracking works at all.
	if len(collided) != 1 {
		t.Fatalf("test setup: collidedQNames = %v, want exactly 1 entry (a/b/__init__.py and a/b.py both module %q)", collided, "a.b")
	}
	const wantCollidedQName = "a.b:foo"
	if !collided[wantCollidedQName] {
		t.Fatalf("collidedQNames = %v, want it to contain %q", collided, wantCollidedQName)
	}
	if _, ok := byQName["caller:main"]; !ok {
		t.Fatal("test setup: caller:main did not resolve in byQName")
	}
	if _, ok := byQName[wantCollidedQName]; !ok {
		t.Fatal("test setup: the collided qname itself did not resolve in byQName (last-wins should still leave SOME id)")
	}

	dbPath := filepath.Join(t.TempDir(), "graph.db")
	seedPrevGraph(t, dbPath,
		[]struct{ qname, pkg string }{
			{"caller:main", "caller"}, {wantCollidedQName, "a.b"},
		},
		[][2]string{{"caller:main", wantCollidedQName}},
	)

	carriedModules := map[string]bool{"caller": true}
	got := mapperCarriedEdges(dbPath, carriedModules, byQName, collided)
	if len(got) != 0 {
		t.Errorf("edge into a collided qname must be DROPPED, not remapped onto the possibly-wrong last-wins row, got %v", got)
	}
}

// TestMapperCarriedEdges_NoCollisionStillCarries is the companion guard: with
// an EMPTY collidedQNames set, the exact same shape of edge must still be
// carried normally — proving the collision check is additive, not a
// blanket suppression of every carried mapper edge.
func TestMapperCarriedEdges_NoCollisionStillCarries(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	seedPrevGraph(t, dbPath,
		[]struct{ qname, pkg string }{
			{"app:fnA", "app"}, {"app:fnB", "app"},
		},
		[][2]string{{"app:fnA", "app:fnB"}},
	)

	carriedModules := map[string]bool{"app": true}
	byQName := map[string]int64{"app:fnA": 701, "app:fnB": 702}
	got := mapperCarriedEdges(dbPath, carriedModules, byQName, nil)
	if len(got) != 1 {
		t.Fatalf("got %d edges, want 1: %v", len(got), got)
	}
	if _, ok := got[[2]int64{701, 702}]; !ok {
		t.Errorf("edge not remapped to the fresh ids: %v", got)
	}
}

// ---------- E.8 (T1/D8): indexerGen bump ----------

// TestManager_IndexerGenBump_RebuildsPriorGraphWithCarryForwardSemantics is
// task E.8, extending D.9's pattern one generation further: a graph.db seeded
// exactly as a PR-D-era build would have left it (indexerGen "3") must be
// treated as stale by the CURRENT stamp, forcing a rebuild under carry-
// forward semantics.
func TestManager_IndexerGenBump_RebuildsPriorGraphWithCarryForwardSemantics(t *testing.T) {
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
	// Same census as the CURRENT tree, but the PR-D-era generation — so a
	// mismatch can only be attributed to indexerGen, not an unrelated census
	// difference.
	oldStamp := stampGen(schema, indexedExtensions(), "3") + ":" + rest
	seedRawGraphMeta(t, dbPath, oldStamp, nil)

	resp, err := m.WaitBuild(ctx, repo, 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Completed || !resp.Rebuilt {
		t.Fatalf("want completed+rebuilt (indexerGen bump must force a rebuild of a PR-D-era graph), got %+v", resp)
	}

	symResp, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "hello"})
	if err != nil {
		t.Fatalf("Symbol hello: %v", err)
	}
	if symResp.Symbol == nil {
		t.Fatal("Symbol hello: nil symbol in response — the rebuild did not run")
	}
}
