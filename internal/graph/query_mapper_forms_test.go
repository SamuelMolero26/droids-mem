package graph

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// pythonRepo writes the minimal dotted-module Python tree the mapper-tier
// query forms are exercised against: a package with a class + method, and a
// sibling module with a function, so package resolution has more than one
// candidate to disambiguate.
func pythonRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "app", "models"), "persona.py",
		"class UserPersonaModel:\n"+
			"    def dominant_persona(self):\n"+
			"        return 0\n")
	writeFile(t, filepath.Join(repo, "app", "models"), "scorer.py",
		"def recommend_diverse(items):\n"+
			"    return items\n")
	return repo
}

// TestSymbol_ResolvesReceiverQualifiedOnMapperQName pins gap 2. The MCP schema
// (internal/mcpserver/graph_tools.go:50) documents the receiver-qualified form
// ('Store.Save') as a supported way to name a symbol, so an agent types
// 'UserPersonaModel.dominant_persona' on a Python repo. It used to miss:
// findSymbol's suffix rung is `qname LIKE '%.'+name`, but the mapper builds
// qnames as modulePath + ":" + container + "." + name (mapper_symbols.go:190),
// so the byte before the container is ':' and the dot-anchored LIKE never
// matches. Missing here is not a harmless miss — it silently degrades to the
// BM25 search fallback, which answers with a *menu* instead of the symbol's
// body, callers, and callees.
func TestSymbol_ResolvesReceiverQualifiedOnMapperQName(t *testing.T) {
	repo := pythonRepo(t)
	m := NewManager(filepath.Join(t.TempDir(), "graphs"))
	t.Cleanup(m.Close)

	resp, err := m.Symbol(context.Background(), SymbolRequest{
		Repo: repo, Symbol: "UserPersonaModel.dominant_persona",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Symbol == nil {
		t.Fatalf("receiver-qualified form degraded to the search fallback; matches=%+v", resp.Matches)
	}
	if want := "app.models.persona:UserPersonaModel.dominant_persona"; resp.Symbol.QName != want {
		t.Errorf("qname = %q, want %q", resp.Symbol.QName, want)
	}
}

// TestSymbol_ReceiverQualifiedStillResolvesInGo guards the Go tier against the
// new ':' rung. Note which rung actually answers here: the Go indexer bakes the
// receiver into the `name` column (index.go:473, `name = recv + "." + method`),
// so "Counter.Inc" resolves at `name = ?` and never reaches either suffix rung
// — verified by deleting the '%.' rung and watching this still pass. That makes
// this a guard on rung ORDER, which is the only way adding a rung could break
// Go, not a guard on the suffix rungs themselves.
func TestSymbol_ReceiverQualifiedStillResolvesInGo(t *testing.T) {
	repo := copyFixture(t)
	writeFile(t, filepath.Join(repo, "zz"), "counter.go",
		"package zz\n\ntype Counter struct{ n int }\n\nfunc (c *Counter) Inc() { c.n++ }\n")
	m := NewManager(filepath.Join(t.TempDir(), "graphs"))
	t.Cleanup(m.Close)

	resp, err := m.Symbol(context.Background(), SymbolRequest{Repo: repo, Symbol: "Counter.Inc"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Symbol == nil {
		t.Fatalf("Go receiver-qualified lookup regressed to search fallback; matches=%+v", resp.Matches)
	}
	if want := "zz.Counter.Inc"; resp.Symbol.QName != want {
		t.Errorf("qname = %q, want %q", resp.Symbol.QName, want)
	}
}

// TestPackage_ResolvesDottedSuffixAndSlashPath pins gap 1. The schema
// (graph_tools.go:98) documents "Package path or suffix, e.g. 'internal/store'
// or just 'store'" — both documented shapes failed on Python because
// Package()'s suffix rung is `package LIKE '%/'+pkg`, slash-anchored, while
// mapper packages for Python are dotted module paths. Every form below names
// the same real package.
func TestPackage_ResolvesDottedSuffixAndSlashPath(t *testing.T) {
	const want = "app.models.scorer"
	for _, form := range []string{
		"app.models.scorer", // exact dotted — the only form that used to work
		"scorer",            // bare suffix, documented
		"app/models/scorer", // slash path, documented
		"models.scorer",     // dotted multi-segment suffix
	} {
		t.Run(form, func(t *testing.T) {
			repo := pythonRepo(t)
			m := NewManager(filepath.Join(t.TempDir(), "graphs"))
			t.Cleanup(m.Close)

			resp, err := m.Package(context.Background(), PackageRequest{Repo: repo, Package: form})
			if err != nil {
				t.Fatalf("Package(%q): %v", form, err)
			}
			if resp.Package != want {
				t.Errorf("Package(%q) resolved to %q, want %q", form, resp.Package, want)
			}
		})
	}
}

// TestPackage_GoFormsUnchanged guards the Go tier: the exact slash path and
// the bare suffix against a slash-separated package must keep resolving. The
// fixture's own packages are single-segment ("testmod", "zz"), so a nested one
// is added here to exercise the slash-suffix rung at all.
func TestPackage_GoFormsUnchanged(t *testing.T) {
	repo := copyFixture(t)
	writeFile(t, filepath.Join(repo, "zz", "deep"), "deep.go", "package deep\n\nfunc Deep() {}\n")
	m := NewManager(filepath.Join(t.TempDir(), "graphs"))
	t.Cleanup(m.Close)

	for _, form := range []string{"zz/deep", "deep"} {
		resp, err := m.Package(context.Background(), PackageRequest{Repo: repo, Package: form})
		if err != nil {
			t.Fatalf("Package(%q): %v", form, err)
		}
		if resp.Package != "zz/deep" {
			t.Errorf("Package(%q) resolved to %q, want zz/deep", form, resp.Package)
		}
	}
}

// TestFence_TagsBySourceLanguage pins gap 5: the fenced `source` body is
// tagged from the symbol's own file extension, so a Python body is not
// announced to the agent as Go.
func TestFence_TagsBySourceLanguage(t *testing.T) {
	for _, tc := range []struct{ file, want string }{
		{"internal/store/save.go", "```go"},
		{"app/models/persona.py", "```python"},
		{"src/app.ts", "```typescript"},
		{"src/view.tsx", "```tsx"},
		{"src/app.js", "```javascript"},
		{"src/view.jsx", "```javascript"},
		{"Makefile", "```"},
	} {
		got := fence("body\n", tc.file)
		if first := firstLine(got); first != tc.want {
			t.Errorf("fence(_, %q) opened with %q, want %q", tc.file, first, tc.want)
		}
	}
}

// TestPackageHint_IsTierAware guards against a Go-shaped promise on a mapper
// answer. The package hint tells the agent how to reach what the surface left
// out; on Go that is "re-query a test symbol by name" (they are indexed), on
// the mapper tier that re-query returns nothing (test files were never
// indexed), so the two tiers must not share one wording.
func TestPackageHint_IsTierAware(t *testing.T) {
	if strings.Contains(pkgSymbolsLimitGo, "not indexed") {
		t.Error("Go hint must not claim test symbols are unindexed — they are queryable by name")
	}
	if !strings.Contains(pkgSymbolsLimitMapper, "not indexed") {
		t.Error("mapper hint must say test files are not indexed at all")
	}
	if strings.Contains(pkgSymbolsLimitMapper, "_test.go") {
		t.Error("mapper hint must not name a Go-specific file pattern")
	}
}

// TestMapperTestFiles_CountedAndDisclosed is the end-to-end proof for the
// mapper tier's half of the test policy, across all three of its languages.
// isMapperTestFile drops these files at walk time, so unlike Go their symbols
// are absent from the graph entirely and every caller count understates. The
// whole chain has to carry that: walk stats → meta.tests_skipped → Freshness →
// rendered answer. Asserted on a SYMBOL query, because the understated number
// it qualifies (transitive_callers) lives there, not on the package surface.
func TestMapperTestFiles_CountedAndDisclosed(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "src"), "api.ts",
		"export function real(): number { return 1; }\n")
	// One per mapper language + one directory-segment case, so the count is
	// only correct if every discovery pattern fired.
	writeFile(t, filepath.Join(repo, "src"), "api.test.ts",
		"import { real } from \"./api\";\nexport function tsTest() { return real(); }\n")
	writeFile(t, filepath.Join(repo, "src"), "api.spec.js",
		"export function jsSpec() { return 1; }\n")
	writeFile(t, filepath.Join(repo, "src"), "test_api.py",
		"def test_api():\n    return 1\n")
	writeFile(t, filepath.Join(repo, "tests"), "helper.py",
		"def helper():\n    return 1\n")

	m := NewManager(filepath.Join(t.TempDir(), "graphs"))
	t.Cleanup(m.Close)
	resp, err := m.Symbol(context.Background(), SymbolRequest{Repo: repo, Symbol: "real"})
	if err != nil {
		t.Fatalf("Symbol: %v", err)
	}
	if resp.Freshness.TestsSkipped != 4 {
		t.Errorf("TestsSkipped = %d, want 4 (.test.ts, .spec.js, test_*.py, tests/ segment)",
			resp.Freshness.TestsSkipped)
	}
	out := RenderSymbol(resp)
	if !strings.Contains(out, "tests_skipped: 4") {
		t.Errorf("rendered answer hides the exclusion:\n%s", out)
	}
	if !strings.Contains(out, "understate") {
		t.Errorf("rendered answer must name the consequence, not just the count:\n%s", out)
	}
}

// TestGoRepo_NeverReportsTestsSkipped keeps the two policies from bleeding
// into each other: the Go tier indexes its test declarations, so a pure Go
// repo must pay nothing for the mapper tier's disclosure.
func TestGoRepo_NeverReportsTestsSkipped(t *testing.T) {
	m, repo := testManagerAt(t, "testdata/pkgtest")
	resp, err := m.Package(context.Background(), PackageRequest{Repo: repo, Package: "pkgtest"})
	if err != nil {
		t.Fatalf("Package: %v", err)
	}
	if resp.Freshness.TestsSkipped != 0 {
		t.Errorf("TestsSkipped = %d on a Go repo, want 0", resp.Freshness.TestsSkipped)
	}
	if !strings.Contains(resp.Hint, "re-query either by its name") {
		t.Errorf("Go package must get the Go hint, got: %s", resp.Hint)
	}
}
