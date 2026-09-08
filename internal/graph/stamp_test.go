package graph

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestStamp_TestFileEditMovesStamp pins the Tests:true lockstep requirement
// (stamp.go's own comment already said this filter must drop "in lockstep"
// once test indexing is enabled): once the semantic tier loads packages with
// Tests:true, declarations inside _test.go files are indexed as symbols and
// participate as callers, so an edit to a _test.go file that adds a new
// caller MUST move the build stamp — otherwise a new test caller is silently
// never picked up because the cache never invalidates.
func TestStamp_TestFileEditMovesStamp(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module x\n\ngo 1.21\n")
	writeFile(t, repo, "a.go", "package x\n\nfunc A() {}\n")
	writeFile(t, repo, "a_test.go", "package x\n")

	base, err := stamp(repo)
	if err != nil {
		t.Fatal(err)
	}

	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(filepath.Join(repo, "a_test.go"), future, future); err != nil {
		t.Fatal(err)
	}
	if s, err := stamp(repo); err != nil {
		t.Fatal(err)
	} else if s == base {
		t.Errorf("test-file edit did not move stamp: still %q; with Tests:true a _test.go edit must invalidate the cache", base)
	}
}

// TestStamp_MapperOnlyEditMovesStamp is task B.5/B.6: the census walk must
// cover indexedExtensions(), not just .go, so a mapper-only file change moves
// the stamp and triggers a rebuild even when no .go file changed at all.
func TestStamp_MapperOnlyEditMovesStamp(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "app.ts", "export const x = 1;\n")

	base, err := stamp(repo)
	if err != nil {
		t.Fatal(err)
	}

	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(filepath.Join(repo, "app.ts"), future, future); err != nil {
		t.Fatal(err)
	}
	if s, err := stamp(repo); err != nil {
		t.Fatal(err)
	} else if s == base {
		t.Errorf("a .ts-only edit did not move the stamp: still %q; census must cover indexedExtensions(), not just .go", base)
	}
}

func TestManager_AliasRootConfigEditInvalidatesGraph(t *testing.T) {
	for _, configName := range []string{"tsconfig.json", "jsconfig.json"} {
		t.Run(configName, func(t *testing.T) {
			repo := t.TempDir()
			writeAliasInvalidationFixture(t, repo)
			writeFile(t, repo, configName, aliasConfigFor("a"))

			m := managerFor(t)
			if _, err := m.Index(context.Background(), repo); err != nil {
				t.Fatalf("initial index: %v", err)
			}
			assertCallerTarget(t, m, repo, "a/target.ts")

			before, err := stamp(repo)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, repo, configName, aliasConfigFor("b"))
			touchFuture(t, filepath.Join(repo, configName))
			after, err := stamp(repo)
			if err != nil {
				t.Fatal(err)
			}
			if after == before {
				t.Fatalf("size-preserving %s edit did not move stamp: %q", configName, before)
			}

			refreshGraph(t, m, repo)
			assertCallerTarget(t, m, repo, "b/target.ts")
		})
	}
}

func TestManager_AliasExtendsEditInvalidatesGraph(t *testing.T) {
	repo := t.TempDir()
	writeAliasInvalidationFixture(t, repo)
	writeFile(t, repo, "tsconfig.json", `{"extends":"./config/aliases.json"}`)
	writeFile(t, repo, "config/aliases.json", aliasConfigFor("a"))

	m := managerFor(t)
	if _, err := m.Index(context.Background(), repo); err != nil {
		t.Fatalf("initial index: %v", err)
	}
	assertCallerTarget(t, m, repo, "a/target.ts")

	before, err := stamp(repo)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "config/aliases.json", aliasConfigFor("b"))
	touchFuture(t, filepath.Join(repo, "config/aliases.json"))
	after, err := stamp(repo)
	if err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Fatalf("size-preserving inherited config edit did not move stamp: %q", before)
	}

	refreshGraph(t, m, repo)
	assertCallerTarget(t, m, repo, "b/target.ts")
}

func TestManager_AliasConfigAddRemoveInvalidatesFallback(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "target.ts", "export function target() {}\n")
	writeFile(t, repo, "b/target.ts", "export function target() {}\n")
	writeFile(t, repo, "app.ts", "import { target } from \"@/target\";\nexport function caller() { target(); }\n")

	m := managerFor(t)
	if _, err := m.Index(context.Background(), repo); err != nil {
		t.Fatalf("initial index: %v", err)
	}
	assertCallerTarget(t, m, repo, "target.ts")

	beforeAdd, err := stamp(repo)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "tsconfig.json", aliasConfigFor("b"))
	afterAdd, err := stamp(repo)
	if err != nil {
		t.Fatal(err)
	}
	if afterAdd == beforeAdd {
		t.Fatalf("adding tsconfig.json did not move stamp: %q", beforeAdd)
	}
	refreshGraph(t, m, repo)
	assertCallerTarget(t, m, repo, "b/target.ts")

	if err := os.Remove(filepath.Join(repo, "tsconfig.json")); err != nil {
		t.Fatal(err)
	}
	afterRemove, err := stamp(repo)
	if err != nil {
		t.Fatal(err)
	}
	if afterRemove == afterAdd {
		t.Fatalf("removing tsconfig.json did not move stamp: %q", afterAdd)
	}
	refreshGraph(t, m, repo)
	assertCallerTarget(t, m, repo, "target.ts")
}

func TestAliasConfigsAreStampedButNotMapperFiles(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "app.ts", "export function app() {}\n")
	writeFile(t, repo, "tsconfig.json", `{"extends":"./config/aliases.json"}`)
	writeFile(t, repo, "config/aliases.json", aliasConfigFor("src"))

	configFiles := aliasConfigFiles(repo)
	if len(configFiles) != 2 {
		t.Fatalf("aliasConfigFiles = %v, want root and inherited config", configFiles)
	}
	files, _, err := mapperFiles(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].rel != "app.ts" {
		t.Fatalf("mapperFiles = %v, want only app.ts", files)
	}
}

func writeAliasInvalidationFixture(t *testing.T, repo string) {
	t.Helper()
	writeFile(t, repo, "a/target.ts", "export function target() {}\n")
	writeFile(t, repo, "b/target.ts", "export function target() {}\n")
	writeFile(t, repo, "app.ts", "import { target } from \"@/target\";\nexport function caller() { target(); }\n")
}

func aliasConfigFor(dir string) string {
	return `{"compilerOptions":{"baseUrl":".","paths":{"@/*":["` + dir + `/*"]}}}`
}

func touchFuture(t *testing.T, name string) {
	t.Helper()
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(name, future, future); err != nil {
		t.Fatal(err)
	}
}

func refreshGraph(t *testing.T, m *Manager, repo string) {
	t.Helper()
	resp, err := m.Symbol(context.Background(), SymbolRequest{Repo: repo, Symbol: "caller", Direction: "down"})
	if err != nil {
		t.Fatalf("trigger refresh: %v", err)
	}
	if !resp.Freshness.Stale || !resp.Freshness.Rebuilding {
		t.Fatalf("config edit did not trigger Manager rebuild: freshness = %+v", resp.Freshness)
	}
	wait, err := m.WaitBuild(context.Background(), repo, 60*time.Second)
	if err != nil {
		t.Fatalf("wait for refresh: %v", err)
	}
	if !wait.Completed {
		t.Fatalf("refresh did not complete: %+v", wait)
	}
}

func assertCallerTarget(t *testing.T, m *Manager, repo, wantFile string) {
	t.Helper()
	resp, err := m.Symbol(context.Background(), SymbolRequest{Repo: repo, Symbol: "caller", Direction: "down"})
	if err != nil {
		t.Fatalf("Symbol caller: %v", err)
	}
	if len(resp.Callees) != 1 {
		t.Fatalf("caller callees = %v, want one target in %s", resp.Callees, wantFile)
	}
	if got := resp.Callees[0].File; got != wantFile {
		t.Errorf("caller target = %q, want %q", got, wantFile)
	}
}
