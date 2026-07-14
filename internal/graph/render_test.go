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
