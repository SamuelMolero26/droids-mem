package graph

import (
	"strings"
	"testing"
)

func TestRenderSymbol_TableAndFence(t *testing.T) {
	tc := 44
	r := &SymbolResponse{
		Repo: "/repo",
		Symbol: &SymbolInfo{
			QName:     "internal/store.Store.Save",
			Kind:      "method",
			File:      "internal/store/save.go",
			Line:      145,
			Signature: "func (s *Store) Save(ctx context.Context, req SaveRequest) (*SaveResponse, error)",
			// Body contains a Go raw-string literal — the fence must outrun the
			// backticks inside it, or the code block terminates early.
			Source: "func f() string {\n\treturn `a` + `b`\n}",
		},
		Callers: []Neighbor{
			{QName: "internal/store.forceUpdate", Signature: "func forceUpdate(a, b int)", File: "save.go", Line: 201, Depth: 1},
		},
		TransitiveCallers: &tc,
		Hint:              "some hint",
	}
	out := RenderSymbol(r)

	// TOON table header carries the row count and shared field names once.
	if !strings.Contains(out, "callers[1]{qname,signature,loc,depth}:") {
		t.Errorf("missing/incorrect callers header:\n%s", out)
	}
	// A signature with a comma must be quoted so it stays one cell.
	if !strings.Contains(out, `"func forceUpdate(a, b int)"`) {
		t.Errorf("comma'd signature not quoted:\n%s", out)
	}
	// loc merges file:line.
	if !strings.Contains(out, "save.go:201") {
		t.Errorf("loc not merged:\n%s", out)
	}
	if !strings.Contains(out, "transitive_callers: 44") {
		t.Errorf("missing blast count:\n%s", out)
	}

	// The fence must be at least 3 backticks and strictly longer than the
	// longest backtick run in the body (here 1), and must close.
	src := r.Symbol.Source
	fenced := fence(src, r.Symbol.File)
	openLen := len(fenced) - len(strings.TrimLeft(fenced, "`"))
	if openLen < 3 {
		t.Errorf("fence shorter than 3 backticks: %d", openLen)
	}
	if strings.Count(src, strings.Repeat("`", openLen)) != 0 {
		t.Errorf("fence run %d appears inside body — would close early", openLen)
	}
	if !strings.HasSuffix(strings.TrimRight(fenced, "\n"), strings.Repeat("`", openLen)) {
		t.Errorf("fence did not close:\n%s", fenced)
	}
}

// TestRenderSymbol_CallerSplitsAndCarried pins task 6.8 (render half of
// 6.4/6.10): the caller-fidelity splits and the carried flag must appear in
// the rendered output.
func TestRenderSymbol_CallerSplitsAndCarried(t *testing.T) {
	r := &SymbolResponse{
		Repo: "/repo",
		Symbol: &SymbolInfo{
			QName: "internal/store.Store.Save", Kind: "method", File: "save.go", Line: 1, Signature: "func()",
		},
		Callers:             []Neighbor{{QName: "a.b", Signature: "func()", File: "a.go", Line: 1, Depth: 1}},
		CallersInTests:      86,
		CallersViaInterface: 36,
		Carried:             true,
	}
	out := RenderSymbol(r)
	if !strings.Contains(out, "callers_in_tests: 86") {
		t.Errorf("missing callers_in_tests:\n%s", out)
	}
	if !strings.Contains(out, "callers_via_interface: 36") {
		t.Errorf("missing callers_via_interface:\n%s", out)
	}
	if !strings.Contains(out, "carried: true") {
		t.Errorf("missing carried flag:\n%s", out)
	}
}

// TestRenderSymbol_StaleUnitsCappedWithHint pins task 6.8 (spec "Freshness
// reports carried units, capped"): the rendered freshness line must show
// "stale_units[N of M]" (not inline all M) plus the capped names, when the
// build's carried-unit list overflowed the cap.
func TestRenderSymbol_StaleUnitsCappedWithHint(t *testing.T) {
	r := &SymbolResponse{
		Repo: "/repo",
		Freshness: Freshness{
			Stamp:           "v1",
			StaleUnits:      []string{"pkg000", "pkg001", "pkg002", "pkg003", "pkg004"},
			StaleUnitsTotal: 213,
		},
	}
	out := RenderSymbol(r)
	if !strings.Contains(out, "stale_units[5 of 213]") {
		t.Errorf("missing capped stale_units header:\n%s", out)
	}
	if !strings.Contains(out, "pkg000") || !strings.Contains(out, "pkg004") {
		t.Errorf("missing the 5 shown unit names:\n%s", out)
	}
	if strings.Contains(out, "pkg005") {
		t.Errorf("inlined a unit past the cap:\n%s", out)
	}
}

// TestRenderSymbol_StaleWordingNotClaimedWithoutFailure pins task 6.8's
// reword: a Stale freshness with NO IndexError (a benign in-flight rebuild,
// which is what a successful partial build looks like mid-async-rebuild)
// must not claim "no longer type-checks" — that phrase is reserved for a
// genuine recorded build failure (IndexError present).
func TestRenderSymbol_StaleWordingNotClaimedWithoutFailure(t *testing.T) {
	r := &SymbolResponse{Repo: "/repo", Freshness: Freshness{Stamp: "v1", Stale: true, Rebuilding: true}}
	out := RenderSymbol(r)
	if strings.Contains(out, "no longer type-checks") {
		t.Errorf("claimed a type-check failure with no IndexError present:\n%s", out)
	}
	if !strings.Contains(out, "STALE") {
		t.Errorf("dropped the STALE signal entirely:\n%s", out)
	}
}

// TestRenderSymbol_EmptyReasonHint is task B.8: a fresh (non-stale) build
// that indexed zero symbols must surface meta.empty_reason through
// writeFreshness, distinguishing "no indexable symbols found" from a build
// failure.
func TestRenderSymbol_EmptyReasonHint(t *testing.T) {
	r := &SymbolResponse{
		Repo:      "/repo",
		Freshness: Freshness{Stamp: "v1", EmptyReason: "no_indexable_symbols"},
	}
	out := RenderSymbol(r)
	if !strings.Contains(out, "no indexable symbols found") {
		t.Errorf("missing empty-graph hint:\n%s", out)
	}
}

// TestRenderSymbol_EmptyReasonNotShownWhenStale pins the ordering rule from
// D2: a genuine build failure (Stale + IndexError) is already distinguishable
// at the source, so a stale EmptyReason left over from a prior good build
// must not be surfaced alongside a failure — that would misleadingly imply
// the CURRENT (failed) build is the one that was empty-but-healthy.
func TestRenderSymbol_EmptyReasonNotShownWhenStale(t *testing.T) {
	r := &SymbolResponse{
		Repo: "/repo",
		Freshness: Freshness{
			Stamp: "v1", Stale: true, IndexError: "type error",
			EmptyReason: "no_indexable_symbols",
		},
	}
	out := RenderSymbol(r)
	if strings.Contains(out, "no indexable symbols found") {
		t.Errorf("empty-graph hint shown alongside a stale build failure:\n%s", out)
	}
}

func TestRenderPackage_EmptyAndStale(t *testing.T) {
	r := &PackageResponse{
		Repo:      "/repo",
		Package:   "internal/store",
		Freshness: Freshness{Stale: true, IndexError: "type error"},
		Symbols:   nil,
	}
	out := RenderPackage(r)
	if !strings.Contains(out, "freshness: STALE") || !strings.Contains(out, "type error") {
		t.Errorf("stale freshness not surfaced:\n%s", out)
	}
	if !strings.Contains(out, "symbols: none") {
		t.Errorf("empty symbol set not definitive:\n%s", out)
	}
}

// TestRenderSymbol_NeighborTotals pins the AXI §4 contract that a capped
// neighbor list ships its true total. truncatedHint tells the agent to "see
// *_total"; before this, query.go computed CallersTotal/CalleesTotal and the
// renderer dropped them, so the hint pointed at a field no agent ever saw.
func TestRenderSymbol_NeighborTotals(t *testing.T) {
	out := RenderSymbol(&SymbolResponse{
		Repo:         "/r",
		Symbol:       &SymbolInfo{QName: "p.F", Kind: "func", File: "p/f.go", Line: 1, Signature: "func F()"},
		Callers:      []Neighbor{{QName: "p.A", Signature: "func A()", File: "p/a.go", Line: 2}},
		Callees:      []Neighbor{{QName: "p.B", Signature: "func B()", File: "p/b.go", Line: 3}},
		CallersTotal: 91,
		CalleesTotal: 60,
		Truncated:    true,
	})
	for _, want := range []string{"callers_total: 91", "callees_total: 60"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// TestRenderSymbol_NeighborTotalsOmittedWhenNotTruncated keeps the field
// definitive: 0 means "not truncated", so it must not print as a bare zero.
func TestRenderSymbol_NeighborTotalsOmittedWhenNotTruncated(t *testing.T) {
	out := RenderSymbol(&SymbolResponse{
		Repo:    "/r",
		Symbol:  &SymbolInfo{QName: "p.F", Kind: "func", File: "p/f.go", Line: 1, Signature: "func F()"},
		Callers: []Neighbor{{QName: "p.A", Signature: "func A()", File: "p/a.go", Line: 2}},
	})
	if strings.Contains(out, "callers_total") || strings.Contains(out, "callees_total") {
		t.Errorf("totals leaked on an untruncated response:\n%s", out)
	}
}

// TestRenderPackage_TestsCountAndTotal pins the two package-surface counts:
// test symbols are a scalar (never rows), and a capped list carries its total.
func TestRenderPackage_TestsCountAndTotal(t *testing.T) {
	out := RenderPackage(&PackageResponse{
		Repo:         "/r",
		Package:      "p",
		Symbols:      []PackageSymbol{{QName: "p.F", Kind: "func", Signature: "func F()", File: "p/f.go", Line: 1}},
		Unexported:   245,
		Tests:        184,
		SymbolsTotal: 445,
		Truncated:    true,
	})
	for _, want := range []string{"unexported: 245", "tests: 184", "symbols_total: 445"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// TestRenderPackage_ZeroCountsOmitted keeps the common case free: a package
// with no tests and no truncation pays nothing for either field.
func TestRenderPackage_ZeroCountsOmitted(t *testing.T) {
	out := RenderPackage(&PackageResponse{
		Repo:    "/r",
		Package: "p",
		Symbols: []PackageSymbol{{QName: "p.F", Kind: "func", Signature: "func F()", File: "p/f.go", Line: 1}},
	})
	if strings.Contains(out, "tests:") || strings.Contains(out, "symbols_total") {
		t.Errorf("zero counts leaked:\n%s", out)
	}
}

// TestRenderFreshness_TestsSkipped pins the mapper tier's half of the test
// story. Go indexes _test.go declarations and reports them as a package count;
// the mapper tier skips test FILES at walk time (mapper.go isMapperTestFile),
// so they are absent from every answer, including caller counts on symbol
// queries. That is a build-level partiality fact like fanout_capped, so it
// rides on freshness and shows up on both tools.
func TestRenderFreshness_TestsSkipped(t *testing.T) {
	out := RenderSymbol(&SymbolResponse{
		Repo:      "/r",
		Freshness: Freshness{TestsSkipped: 12},
		Symbol:    &SymbolInfo{QName: "src/api:real", Kind: "func", File: "src/api.ts", Line: 1, Signature: "function real()"},
	})
	if !strings.Contains(out, "tests_skipped: 12") {
		t.Errorf("missing tests_skipped in:\n%s", out)
	}
	// The count alone is a bare fact; the agent needs to know it makes caller
	// counts understate, not just that some files were skipped.
	if !strings.Contains(out, "understate") {
		t.Errorf("tests_skipped must say what it costs the agent:\n%s", out)
	}
}

// TestRenderFreshness_TestsSkippedAbsentOnGo keeps the common Go path free:
// nothing is skipped there, so the line must not appear at all.
func TestRenderFreshness_TestsSkippedAbsentOnGo(t *testing.T) {
	out := RenderSymbol(&SymbolResponse{
		Repo:   "/r",
		Symbol: &SymbolInfo{QName: "p.F", Kind: "func", File: "p/f.go", Line: 1, Signature: "func F()"},
	})
	if strings.Contains(out, "tests_skipped") || strings.Contains(out, "freshness:") {
		t.Errorf("clean Go response must carry no freshness line:\n%s", out)
	}
}
