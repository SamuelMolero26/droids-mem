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
	fenced := fence(src)
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
		CallerTestFiles:     19,
		CallersViaInterface: 36,
		Carried:             true,
	}
	out := RenderSymbol(r)
	if !strings.Contains(out, "callers_in_tests: 86") {
		t.Errorf("missing callers_in_tests:\n%s", out)
	}
	if !strings.Contains(out, "caller_test_files: 19") {
		t.Errorf("missing caller_test_files:\n%s", out)
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
// "stale_units[N of M]" (not inline all M) plus the capped names and a hint,
// when the build's carried-unit list overflowed the cap.
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
	if !strings.Contains(out, staleUnitsHint) {
		t.Errorf("missing the full-list hint:\n%s", out)
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
