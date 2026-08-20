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
	idx := buildMapperLadderIndex(syms)

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
	idx := buildMapperLadderIndex(syms)

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
	idx := buildMapperLadderIndex(syms)

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
	idx := buildMapperLadderIndex(syms)

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
	idx := buildMapperLadderIndex(syms)

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
	idx := buildMapperLadderIndex(syms)

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
	idx := buildMapperLadderIndex(syms)

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
	idx := buildMapperLadderIndex(syms)

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
	idx := buildMapperLadderIndex(syms)

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
