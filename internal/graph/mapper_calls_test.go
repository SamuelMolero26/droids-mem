package graph

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	gts "github.com/odvcencio/gotreesitter"
	_ "modernc.org/sqlite"
)

// ---------- D.1: containment attribution ----------

// TestInnermostContainer_NestedCallAttributesToInnerSymbol pins that a call
// nested inside two symbols (an outer class-like container and an inner
// method) attributes to the INNERMOST one, not the outer one — the whole
// point of walking left from the binary-search insertion point (design D3).
func TestInnermostContainer_NestedCallAttributesToInnerSymbol(t *testing.T) {
	outer := mapperSym{row: &symRow{id: 1, name: "Outer"}, start: 0, end: 100, container: ""}
	inner := mapperSym{row: &symRow{id: 2, name: "Inner"}, start: 20, end: 50, container: "Outer"}
	syms := []mapperSym{outer, inner} // deliberately unsorted; innermostContainer sorts internally via caller contract, so pre-sort here

	got, ok := innermostContainer(sortedByStart(syms), 30, 35)
	if !ok {
		t.Fatal("innermostContainer: no container found for a call nested inside Inner")
	}
	if got.row.name != "Inner" {
		t.Errorf("innermostContainer = %q, want %q (must pick the innermost/deepest containing symbol, not Outer)", got.row.name, "Inner")
	}
}

// TestInnermostContainer_AdjacentSymbolsAttributeToTheContainingOne pins that
// two non-overlapping, adjacent top-level symbols each keep their own calls —
// a call inside F2's range must never be misattributed to F1 merely because
// F1 sorts first by start byte.
func TestInnermostContainer_AdjacentSymbolsAttributeToTheContainingOne(t *testing.T) {
	f1 := mapperSym{row: &symRow{id: 1, name: "F1"}, start: 0, end: 50}
	f2 := mapperSym{row: &symRow{id: 2, name: "F2"}, start: 50, end: 100}
	syms := sortedByStart([]mapperSym{f1, f2})

	got, ok := innermostContainer(syms, 70, 80)
	if !ok {
		t.Fatal("innermostContainer: no container found for a call inside F2's own range")
	}
	if got.row.name != "F2" {
		t.Errorf("innermostContainer = %q, want %q — adjacent symbol F1 must not steal a call inside F2's own range", got.row.name, "F2")
	}
}

// TestInnermostContainer_ModuleLevelCallIsDropped pins design D3's explicit
// rule: "a call with no enclosing symbol (module-level statement) is
// dropped, not attributed to the file". A call in the gap between two
// symbols has no enclosing container at all.
func TestInnermostContainer_ModuleLevelCallIsDropped(t *testing.T) {
	f1 := mapperSym{row: &symRow{id: 1, name: "F1"}, start: 0, end: 40}
	f2 := mapperSym{row: &symRow{id: 2, name: "F2"}, start: 60, end: 100}
	syms := sortedByStart([]mapperSym{f1, f2})

	if _, ok := innermostContainer(syms, 50, 55); ok {
		t.Error("innermostContainer: a call at module level (in the gap between F1 and F2) must be dropped, not attributed to either symbol")
	}
}

// TestAttributeMapperCalls_DropsModuleLevelCallEndToEnd exercises
// attributeMapperCalls itself (not just the low-level helper), pinning that
// module-level calls never surface as a mapperCallsite at all.
func TestAttributeMapperCalls_DropsModuleLevelCallEndToEnd(t *testing.T) {
	f1 := mapperSym{row: &symRow{id: 1, name: "F1", file: "a.ts"}, start: 0, end: 10}
	mapperSyms := []mapperSym{f1}
	fileCalls := []mapperFileCalls{
		{file: "a.ts", lang: "typescript", refs: []gts.CallRef{{Name: "helper", StartByte: 30, EndByte: 36}}},
	}
	got := attributeMapperCalls(mapperSyms, fileCalls)
	if len(got) != 0 {
		t.Errorf("attributeMapperCalls returned %d callsites, want 0 (the only call is module-level, outside F1's range)", len(got))
	}
}

// sortedByStart is a tiny test helper mirroring the sort attributeMapperCalls
// performs internally per file — innermostContainer's own contract assumes
// its input is already sorted ascending by start.
func sortedByStart(syms []mapperSym) []mapperSym {
	out := append([]mapperSym(nil), syms...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].start > out[j].start; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// ---------- D.3: resolution ladder rungs ----------

// TestLadder_Rung0_FiltersByReceiverArityButContinuesWalk pins rung 0's
// ASSIGN-not-return asymmetry (design D4): it narrows the candidate set and
// the walk CONTINUES through the later rungs against that narrowed set,
// unlike every other rung which returns immediately on a hit.
func TestLadder_Rung0_FiltersByReceiverArityButContinuesWalk(t *testing.T) {
	free := &symRow{id: 1, name: "get", file: "a.ts", pkg: "a"}
	member := &symRow{id: 2, name: "get", file: "a.ts", pkg: "a"}
	syms := []mapperSym{
		{row: free, container: ""},         // free function: no container
		{row: member, container: "Client"}, // member: has a container
	}
	idx := buildMapperLadderIndex(syms, nil)

	c := mapperCallsite{name: "get", receiver: "", file: "a.ts", pkg: "a", lang: "typescript"}
	hits, total := idx.resolve(c)
	if len(hits) != 1 || total != 1 {
		t.Fatalf("resolve() hits=%d total=%d, want exactly 1 — rung 0 must narrow to the free function and the walk must continue against that narrowed set, not the original 2 (same-file would otherwise match both)", len(hits), total)
	}
	if idx.syms[hits[0]].row.id != free.id {
		t.Errorf("resolved id = %d, want the free function %d — rung 0's arity filter must exclude the member candidate", idx.syms[hits[0]].row.id, free.id)
	}
}

// TestLadder_Rung1_ThisResolvesToEnclosingClassMember pins rung 1: a
// this/self receiver resolves to the caller's own enclosing class's member.
func TestLadder_Rung1_ThisResolvesToEnclosingClassMember(t *testing.T) {
	widgetMember := &symRow{id: 1, name: "helper", file: "a.ts", pkg: "a"}
	unrelated := &symRow{id: 2, name: "helper", file: "b.ts", pkg: "b"}
	syms := []mapperSym{
		{row: widgetMember, container: "Widget"},
		{row: unrelated, container: "Other"},
	}
	idx := buildMapperLadderIndex(syms, nil)

	c := mapperCallsite{name: "helper", receiver: "this", container: "Widget.render", file: "a.ts", pkg: "a", lang: "typescript"}
	hits, total := idx.resolve(c)
	if len(hits) != 1 || total != 1 {
		t.Fatalf("resolve() hits=%d total=%d, want exactly 1 (this.helper() must resolve to Widget's own member only)", len(hits), total)
	}
	if idx.syms[hits[0]].row.id != widgetMember.id {
		t.Errorf("resolved id = %d, want Widget's helper %d", idx.syms[hits[0]].row.id, widgetMember.id)
	}
}

// TestLadder_Rung2b_ReceiverNamesKnownClassRepoWide pins rung 2b: a receiver
// naming a known class (repo-wide, no import scoping — that's rung 2a,
// PR-G2) resolves to that class's member even from an unrelated file.
func TestLadder_Rung2b_ReceiverNamesKnownClassRepoWide(t *testing.T) {
	classDef := &symRow{id: 1, name: "Client", kind: "class", file: "a.ts", pkg: "a"}
	method := &symRow{id: 2, name: "get", file: "a.ts", pkg: "a"}
	other := &symRow{id: 3, name: "get", file: "b.ts", pkg: "b"}
	syms := []mapperSym{
		{row: classDef, container: ""},
		{row: method, container: "Client"},
		{row: other, container: "Other"},
	}
	idx := buildMapperLadderIndex(syms, nil)

	c := mapperCallsite{name: "get", receiver: "Client", file: "z.ts", pkg: "z", lang: "typescript"}
	hits, total := idx.resolve(c)
	if len(hits) != 1 || total != 1 {
		t.Fatalf("resolve() hits=%d total=%d, want exactly 1 (Client.get() must resolve to Client's own member)", len(hits), total)
	}
	if idx.syms[hits[0]].row.id != method.id {
		t.Errorf("resolved id = %d, want Client.get %d", idx.syms[hits[0]].row.id, method.id)
	}
}

// TestLadder_Rung3_SameFileWinsWhenNoReceiverMatch pins rung 3: with no
// receiver-driven match, a same-file candidate wins over one in another file.
func TestLadder_Rung3_SameFileWinsWhenNoReceiverMatch(t *testing.T) {
	sameFile := &symRow{id: 1, name: "helper", file: "a.ts", pkg: "a"}
	otherFile := &symRow{id: 2, name: "helper", file: "b.ts", pkg: "a"}
	syms := []mapperSym{{row: sameFile}, {row: otherFile}}
	idx := buildMapperLadderIndex(syms, nil)

	c := mapperCallsite{name: "helper", receiver: "", file: "a.ts", pkg: "a", lang: "typescript"}
	hits, total := idx.resolve(c)
	if len(hits) != 1 || total != 1 {
		t.Fatalf("resolve() hits=%d total=%d, want exactly 1 (same-file candidate)", len(hits), total)
	}
	if idx.syms[hits[0]].row.id != sameFile.id {
		t.Errorf("resolved id = %d, want the same-file candidate %d", idx.syms[hits[0]].row.id, sameFile.id)
	}
}

// TestLadder_Rung4_SamePackageWinsWhenNoFileMatch pins rung 4: when no
// candidate shares the caller's file, a same-package candidate wins.
func TestLadder_Rung4_SamePackageWinsWhenNoFileMatch(t *testing.T) {
	samePkg := &symRow{id: 1, name: "helper", file: "sub/a.ts", pkg: "pkg"}
	otherPkg := &symRow{id: 2, name: "helper", file: "other/b.ts", pkg: "otherpkg"}
	syms := []mapperSym{{row: samePkg}, {row: otherPkg}}
	idx := buildMapperLadderIndex(syms, nil)

	c := mapperCallsite{name: "helper", receiver: "", file: "sub/caller.ts", pkg: "pkg", lang: "typescript"}
	hits, total := idx.resolve(c)
	if len(hits) != 1 || total != 1 {
		t.Fatalf("resolve() hits=%d total=%d, want exactly 1 (same-package candidate)", len(hits), total)
	}
	if idx.syms[hits[0]].row.id != samePkg.id {
		t.Errorf("resolved id = %d, want the same-package candidate %d", idx.syms[hits[0]].row.id, samePkg.id)
	}
}

// TestLadder_Rung5_RepoWideFallbackWhenNothingElseMatches pins rung 5's
// unfiltered fallback: when no earlier rung narrows the set at all, every
// same-named candidate repo-wide is returned (over-approximation contract).
func TestLadder_Rung5_RepoWideFallbackWhenNothingElseMatches(t *testing.T) {
	a := &symRow{id: 1, name: "helper", file: "a.ts", pkg: "p1"}
	b := &symRow{id: 2, name: "helper", file: "b.ts", pkg: "p2"}
	syms := []mapperSym{{row: a}, {row: b}}
	idx := buildMapperLadderIndex(syms, nil)

	c := mapperCallsite{name: "helper", receiver: "", file: "z.ts", pkg: "z", lang: "typescript"}
	hits, total := idx.resolve(c)
	if len(hits) != 2 || total != 2 {
		t.Fatalf("resolve() hits=%d total=%d, want 2 (repo-wide fallback, nothing else matched, nothing capped)", len(hits), total)
	}
}

// TestLadder_RungMatchingNothingNeverZeroesCandidates is the spec's own
// scenario, verbatim: rung 3 (same file) matching zero candidates must NOT
// leave rung 4 (same package) with an empty set to filter — it must see the
// un-narrowed set from before rung 3 ran.
func TestLadder_RungMatchingNothingNeverZeroesCandidates(t *testing.T) {
	a := &symRow{id: 1, name: "helper", file: "a.ts", pkg: "pkg"}
	b := &symRow{id: 2, name: "helper", file: "b.ts", pkg: "pkg"}
	syms := []mapperSym{{row: a}, {row: b}}
	idx := buildMapperLadderIndex(syms, nil)

	// Caller is in neither a.ts nor b.ts, so rung 3 (same file) matches zero.
	c := mapperCallsite{name: "helper", receiver: "", file: "caller.ts", pkg: "pkg", lang: "typescript"}
	hits, total := idx.resolve(c)
	if len(hits) != 2 || total != 2 {
		t.Fatalf("resolve() hits=%d total=%d, want 2 — rung 3 matching zero must not zero the candidate set feeding rung 4", len(hits), total)
	}
}

// ---------- D.5: labeled fan-out truncation ----------

// TestLadder_Rung5_FanoutCappedLabelsTruncationWithoutSilentSlice is the
// spec's exact scenario: 12 rung-5 candidates, fanoutCap=8 -> 8 hits plus the
// full pre-cap total (12) recoverable, never a silent slice.
func TestLadder_Rung5_FanoutCappedLabelsTruncationWithoutSilentSlice(t *testing.T) {
	var syms []mapperSym
	for i := 0; i < 12; i++ {
		syms = append(syms, mapperSym{row: &symRow{
			id: int64(i + 1), name: "get", file: fmt.Sprintf("f%d.ts", i), pkg: fmt.Sprintf("p%d", i),
		}})
	}
	idx := buildMapperLadderIndex(syms, nil)

	c := mapperCallsite{name: "get", receiver: "", file: "z.ts", pkg: "z", lang: "typescript"}
	hits, total := idx.resolve(c)
	if len(hits) != fanoutCap {
		t.Errorf("resolve() returned %d hits, want exactly fanoutCap (%d) — never a silent unlabeled slice", len(hits), fanoutCap)
	}
	if total != 12 {
		t.Errorf("resolve() total = %d, want 12 — the full pre-cap candidate count must be recoverable", total)
	}
}

// TestLadder_Rung5_AtOrUnderCapCarriesNoTruncation pins the companion
// scenario: a candidate set at or below fanoutCap reports hits==total, i.e.
// no truncation signal.
func TestLadder_Rung5_AtOrUnderCapCarriesNoTruncation(t *testing.T) {
	var syms []mapperSym
	for i := 0; i < fanoutCap; i++ {
		syms = append(syms, mapperSym{row: &symRow{
			id: int64(i + 1), name: "get", file: fmt.Sprintf("f%d.ts", i), pkg: fmt.Sprintf("p%d", i),
		}})
	}
	idx := buildMapperLadderIndex(syms, nil)

	c := mapperCallsite{name: "get", receiver: "", file: "z.ts", pkg: "z", lang: "typescript"}
	hits, total := idx.resolve(c)
	if len(hits) != total {
		t.Errorf("resolve() hits=%d total=%d, want equal when at-or-under cap (no truncation)", len(hits), total)
	}
}

// TestBuildIndex_FanoutCapped_LabelsMetaWithTruncatedCallsiteCount is D.6's
// end-to-end wiring pin: a real build whose one bare call resolves to 12
// rung-5 candidates must emit exactly fanoutCap edges from that caller AND
// record meta.fanout_capped=1 (one CALLSITE truncated, not one edge dropped).
func TestBuildIndex_FanoutCapped_LabelsMetaWithTruncatedCallsiteCount(t *testing.T) {
	repo := t.TempDir()
	for i := 0; i < 12; i++ {
		writeFile(t, repo, fmt.Sprintf("mod%d.ts", i), "export function target(): void {}\n")
	}
	writeFile(t, repo, "caller.ts", "export function run(): void { target(); }\n")

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

	var raw string
	if err := conn.QueryRow(`SELECT value FROM meta WHERE key = 'fanout_capped'`).Scan(&raw); err != nil {
		t.Fatalf("read meta.fanout_capped: %v", err)
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("meta.fanout_capped = %q, not an integer: %v", raw, err)
	}
	if n != 1 {
		t.Errorf("meta.fanout_capped = %d, want 1 (exactly one callsite truncated)", n)
	}

	var edgeCount int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM edges e JOIN symbols s ON s.id = e.caller WHERE s.name = 'run'`).Scan(&edgeCount); err != nil {
		t.Fatal(err)
	}
	if edgeCount != fanoutCap {
		t.Errorf("edges from run() = %d, want exactly fanoutCap (%d), never a silent partial or full slice", edgeCount, fanoutCap)
	}
}

// ---------- D.7: tier-disjointness SQL guard ----------

// TestEdges_NeverJoinGoSymbolToMapperSymbol is task D.7 — the foundational
// precondition PR-F's zero-SQL precision derivation depends on: a symbol's
// entire transitive closure must be single-tier, so no edge may have one
// endpoint in a .go file and the other in a mapper-extension file. This must
// hold by construction (callEdges only ever pairs Go-tier ids from byPos;
// mapperEdges only ever pairs mapper-tier ids from mapperSyms) — this test
// pins the invariant directly rather than assuming it from tier separation
// elsewhere.
func TestEdges_NeverJoinGoSymbolToMapperSymbol(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module x\n\ngo 1.21\n")
	writeFile(t, repo, "main.go", "package main\n\nfunc Hi() { Bye() }\nfunc Bye() {}\n")
	writeFile(t, repo, "app.ts", "export function hello(): void { helper(); }\nexport function helper(): void {}\n")

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

	var mixed int
	err = conn.QueryRow(`SELECT COUNT(*) FROM edges e
		JOIN symbols s1 ON s1.id = e.caller
		JOIN symbols s2 ON s2.id = e.callee
		WHERE (s1.file LIKE '%.go') != (s2.file LIKE '%.go')`).Scan(&mixed)
	if err != nil {
		t.Fatal(err)
	}
	if mixed != 0 {
		t.Errorf("found %d edge(s) joining a .go symbol to a mapper-extension symbol, want 0 — tier disjointness broken", mixed)
	}

	// Sanity: both tiers actually produced at least one edge, so the zero
	// above proves disjointness rather than an empty build.
	var goEdges, mapperEdgeCount int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM edges e JOIN symbols s ON s.id = e.caller WHERE s.file LIKE '%.go'`).Scan(&goEdges); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(`SELECT COUNT(*) FROM edges e JOIN symbols s ON s.id = e.caller WHERE s.file LIKE '%.ts'`).Scan(&mapperEdgeCount); err != nil {
		t.Fatal(err)
	}
	if goEdges == 0 || mapperEdgeCount == 0 {
		t.Fatalf("test setup: goEdges=%d mapperEdges=%d, want both > 0 so the disjointness check is meaningful", goEdges, mapperEdgeCount)
	}
}

// ---------- D.9 (T1/D8): indexerGen bump ----------

// TestManager_IndexerGenBump_RebuildsPriorGraphWithCallEdges is task D.9,
// extending C.6's pattern one generation further: a graph.db seeded exactly
// as a PR-C-era build would have left it (indexerGen "2" — symbols only, no
// call edges) must be treated as stale by the CURRENT stamp, forcing a
// rebuild that now returns mapper CALL EDGES a PR-C-era build could never
// have produced.
func TestManager_IndexerGenBump_RebuildsPriorGraphWithCallEdges(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "app.ts", "export function helper(): void {}\nexport function run(): void { helper(); }\n")

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
	// Same census as the CURRENT tree, but the PR-C-era generation — so a
	// mismatch can only be attributed to indexerGen, not an unrelated census
	// difference.
	oldStamp := stampGen(schema, indexedExtensions(), "2") + ":" + rest
	seedRawGraphMeta(t, dbPath, oldStamp, nil)

	resp, err := m.WaitBuild(ctx, repo, 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Completed || !resp.Rebuilt {
		t.Fatalf("want completed+rebuilt (indexerGen bump must force a rebuild of a PR-C-era graph), got %+v", resp)
	}

	symResp, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "helper"})
	if err != nil {
		t.Fatalf("Symbol helper: %v", err)
	}
	if len(symResp.Callers) == 0 {
		t.Fatal("Symbol helper: no callers in response — the rebuild did not produce mapper call edges")
	}
}

// ---------- G2.4 / G2.5: module-specifier -> repo-file resolution ----------

// TestResolveSpecifier_RelativeFormsAndBareMiss covers G2.4 and G2.5 in one
// table: a relative specifier resolves against the importer's own directory
// via extension probing and index-file fallback, and anything that names no
// repo file — a bare package specifier above all — resolves to "" WITHOUT an
// error. That empty result is not a failure path: it is exactly how a rung
// 2a miss is expressed, and the ladder treats a miss as "fall through
// un-narrowed", never as "no candidates".
func TestResolveSpecifier_RelativeFormsAndBareMiss(t *testing.T) {
	known := map[string]bool{
		"src/bar.ts":         true,
		"src/widget.tsx":     true,
		"src/legacy.js":      true,
		"src/util/index.ts":  true,
		"src/esm.mjs":        true,
		"other/bar.ts":       true,
		"src/explicit.js":    true,
		"node_modules/ax.ts": true,
	}
	cases := []struct {
		name     string
		importer string
		spec     string
		want     string
	}{
		{"relative ts", "src/a.ts", "./bar", "src/bar.ts"},
		{"relative tsx", "src/a.ts", "./widget", "src/widget.tsx"},
		{"relative js", "src/a.ts", "./legacy", "src/legacy.js"},
		{"relative mjs", "src/a.ts", "./esm", "src/esm.mjs"},
		{"index file", "src/a.ts", "./util", "src/util/index.ts"},
		{"parent dir", "src/deep/a.ts", "../bar", "src/bar.ts"},
		{"dot-dot escapes to sibling tree", "src/a.ts", "../other/bar", "other/bar.ts"},
		{"specifier already carries its extension", "src/a.ts", "./explicit.js", "src/explicit.js"},
		{"bare specifier resolves to nothing", "src/a.ts", "axios", ""},
		{"scoped bare specifier resolves to nothing", "src/a.ts", "@scope/pkg", ""},
		{"relative miss resolves to nothing", "src/a.ts", "./nope", ""},
		{"escaping the repo root resolves to nothing", "a.ts", "../../outside", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveSpecifier(tc.importer, tc.spec, known); got != tc.want {
				t.Errorf("resolveSpecifier(%q, %q) = %q, want %q", tc.importer, tc.spec, got, tc.want)
			}
		})
	}
}

// TestResolveSpecifier_PrefersDirectFileOverIndexFile pins the probe order:
// when both `./util.ts` and `./util/index.ts` exist, the direct file wins.
// Getting this backwards would point rung 2a at the wrong file whenever a
// module has both forms — a real layout in TS repos, not a contrived one.
func TestResolveSpecifier_PrefersDirectFileOverIndexFile(t *testing.T) {
	known := map[string]bool{"src/util.ts": true, "src/util/index.ts": true}
	if got := resolveSpecifier("src/a.ts", "./util", known); got != "src/util.ts" {
		t.Errorf("resolveSpecifier = %q, want the direct file %q", got, "src/util.ts")
	}
}

// ---------- G2.7: ladder rung 2a (import-scoped receiver) ----------

// rung2aFixture builds the one shared shape both rung-2a tests need: two
// candidates named "get", one in the file an import points at (under class
// Client) and one under a repo-wide class literally named "Bar". Rung 2b
// keys on the receiver NAME, so with a receiver of "Bar" it necessarily
// picks the second — which makes the two rungs give provably different
// answers on identical input, the only way a test can tell which one fired.
func rung2aFixture() (syms []mapperSym, importedGet, repoWideGet *symRow) {
	clientClass := &symRow{id: 1, name: "Client", kind: "class", file: "x.ts", pkg: "x"}
	importedGet = &symRow{id: 2, name: "get", file: "x.ts", pkg: "x"}
	barClass := &symRow{id: 3, name: "Bar", kind: "class", file: "z.ts", pkg: "z"}
	repoWideGet = &symRow{id: 4, name: "get", file: "z.ts", pkg: "z"}
	return []mapperSym{
		{row: clientClass, container: ""},
		{row: importedGet, container: "Client"},
		{row: barClass, container: ""},
		{row: repoWideGet, container: "Bar"},
	}, importedGet, repoWideGet
}

// TestLadder_Rung2a_ImportScopedReceiverStopsWalkBeforeRung2b is the first
// half of G2.7. `Bar` is bound in a.ts to a module resolving to x.ts, so the
// call Bar.get() must resolve to x.ts's member — NOT to the repo-wide class
// also named Bar in z.ts, which is what rung 2b would return. Getting z.ts
// back means rung 2a did not fire, or did not stop the walk.
func TestLadder_Rung2a_ImportScopedReceiverStopsWalkBeforeRung2b(t *testing.T) {
	syms, importedGet, repoWideGet := rung2aFixture()
	idx := buildMapperLadderIndex(syms, map[string]map[string]string{
		"a.ts": {"Bar": "x.ts"},
	})

	c := mapperCallsite{name: "get", receiver: "Bar", file: "a.ts", pkg: "a", lang: "typescript"}
	hits, total := idx.resolve(c)
	if len(hits) != 1 || total != 1 {
		t.Fatalf("resolve() hits=%d total=%d, want exactly 1", len(hits), total)
	}
	got := idx.syms[hits[0]].row.id
	if got == repoWideGet.id {
		t.Fatalf("resolved to the repo-wide class's member (id %d): rung 2b answered, so rung 2a either did not fire or did not stop the walk", got)
	}
	if got != importedGet.id {
		t.Errorf("resolved id = %d, want the imported file's member %d", got, importedGet.id)
	}
}

// TestLadder_Rung2aMiss_FallsThroughUnNarrowed is the second half of G2.7 and
// the load-bearing one. A rung whose filter matches nothing must be
// DISCARDED, leaving the walk to continue against the candidate set from
// BEFORE it ran — a rung that narrowed destructively on a miss could zero the
// set and lose a caller that can fire. Two miss shapes are covered: a binding
// pointing at a file with no matching candidate, and no binding at all (the
// bare-specifier case from G2.5, where resolveSpecifier returned ""). Both
// must land on exactly what the ladder returns with rung 2a's input absent
// entirely — the nil-imports index is the control.
func TestLadder_Rung2aMiss_FallsThroughUnNarrowed(t *testing.T) {
	syms, _, repoWideGet := rung2aFixture()
	c := mapperCallsite{name: "get", receiver: "Bar", file: "a.ts", pkg: "a", lang: "typescript"}

	control, controlTotal := buildMapperLadderIndex(syms, nil).resolve(c)
	if len(control) != 1 || idxRowID(buildMapperLadderIndex(syms, nil), control[0]) != repoWideGet.id {
		t.Fatalf("control (no imports at all) did not land on rung 2b's answer; fixture is wrong, not the code")
	}

	cases := map[string]map[string]map[string]string{
		"binding resolves to a file holding no candidate": {"a.ts": {"Bar": "unrelated.ts"}},
		"specifier resolved to nothing, so no binding":    {"a.ts": {}},
		"the importing file imported nothing":             {},
	}
	for name, imports := range cases {
		t.Run(name, func(t *testing.T) {
			idx := buildMapperLadderIndex(syms, imports)
			hits, total := idx.resolve(c)
			if len(hits) != len(control) || total != controlTotal {
				t.Fatalf("hits=%d total=%d, want the control's %d/%d — a rung-2a miss must leave the candidate set exactly as it was", len(hits), total, len(control), controlTotal)
			}
			if idx.syms[hits[0]].row.id != repoWideGet.id {
				t.Errorf("resolved id = %d, want rung 2b's answer %d", idx.syms[hits[0]].row.id, repoWideGet.id)
			}
		})
	}
}

func idxRowID(ix *mapperLadderIndex, i int) int64 { return ix.syms[i].row.id }

// TestBuildIndex_Rung2a_ResolvesThroughRealImport is the end-to-end pin for
// G2.8's wiring, and it exists for the same reason PR-E's E.6 did: rung 2a's
// input arrives from a DIFFERENT function (mapperImports' bindings, routed
// through resolveBindings), so a test that only exercises the ladder with a
// hand-built map passes identically against a build where that map never
// arrives. This one runs the real buildIndex.
//
// The fixture is the collision that makes the two rungs distinguishable:
// a.ts imports Client under the local alias `Bar`, and an UNRELATED class
// literally named Bar exists in z.ts. Rung 2b matches on the receiver name,
// so it would answer z.ts; only rung 2a, reading a.ts's own import, answers
// client.ts.
func TestBuildIndex_Rung2a_ResolvesThroughRealImport(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "client.ts", "export class Client {\n  static get(): void {}\n}\n")
	writeFile(t, repo, "z.ts", "export class Bar {\n  static get(): void {}\n}\n")
	writeFile(t, repo, "a.ts", "import { Client as Bar } from \"./client\";\n\nexport function run(): void { Bar.get(); }\n")

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

	rows, err := conn.Query(`SELECT s2.file FROM edges e
		JOIN symbols s1 ON s1.id = e.caller
		JOIN symbols s2 ON s2.id = e.callee
		WHERE s1.name = 'run' AND s2.name = 'get'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var files []string
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	if len(files) != 1 {
		t.Fatalf("edges run()->get() land in %v, want exactly one (client.ts) — more than one means rung 2a did not narrow", files)
	}
	if files[0] != "client.ts" {
		t.Errorf("run()->get() resolved into %q, want %q: rung 2a's import scoping did not reach the real build", files[0], "client.ts")
	}
}

// ---------- G2.9 (T1/D8): indexerGen bump ----------

// TestManager_IndexerGenBump_RebuildsPriorGraphWithImportScopedLadder
// extends D.9/E.8's pattern one generation further. A graph seeded exactly
// as a PR-G1-era build would have left it (indexerGen "4") must be treated
// as stale, because rung 2a changes which EDGES a build writes: a pre-G2
// graph holds edges resolved without import scoping, differently attributed
// rather than merely less complete.
func TestManager_IndexerGenBump_RebuildsPriorGraphWithImportScopedLadder(t *testing.T) {
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
	// Same census as the CURRENT tree, so a mismatch can only be attributed
	// to indexerGen, not an unrelated census difference.
	oldStamp := stampGen(schema, indexedExtensions(), "4") + ":" + rest
	seedRawGraphMeta(t, dbPath, oldStamp, nil)

	resp, err := m.WaitBuild(ctx, repo, 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Completed || !resp.Rebuilt {
		t.Fatalf("want completed+rebuilt (indexerGen bump must force a rebuild of a PR-G1-era graph), got %+v", resp)
	}
}
