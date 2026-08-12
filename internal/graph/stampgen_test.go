package graph

import (
	"path/filepath"
	"strings"
	"testing"
)

// ensureFresh gates entirely on `fresh.Stamp == current`, and graph.db stores
// no schema version. So editing the schema does NOT invalidate a cached graph:
// a repo whose file census is unchanged keeps being served rows written under
// the old shape until some unrelated edit moves the stamp. Deriving the stamp's
// generation from the schema text closes that by construction — you cannot
// forget to bump what you never write by hand.
func TestStampGen_ChangesWithSchema(t *testing.T) {
	a := stampGen("CREATE TABLE x(a INT);", []string{".go"})
	b := stampGen("CREATE TABLE x(a INT, b INT);", []string{".go"})
	if a == b {
		t.Errorf("stampGen ignored a schema change: both %q", a)
	}
	if !strings.HasPrefix(a, "v") {
		t.Errorf("stampGen = %q, want a leading %q", a, "v")
	}
}

// The same argument applies to what the walk covers: when the indexed
// extension set grows, every cached graph was built from a narrower file set
// and must be rebuilt, even though the old files are untouched.
func TestStampGen_ChangesWithIndexedExtensions(t *testing.T) {
	a := stampGen("CREATE TABLE x(a INT);", []string{".go"})
	b := stampGen("CREATE TABLE x(a INT);", []string{".go", ".ts"})
	if a == b {
		t.Errorf("stampGen ignored an extension-set change: both %q", a)
	}
}

// Determinism is the property the whole cache rests on, and it fails far more
// expensively than a missed invalidation: a generation that varies between
// calls never matches the stored stamp, so EVERY query rebuilds the graph, for
// every repo, forever. Extension order is part of that — the indexed set is a
// set, not a sequence, so sourcing it from a map some day must not silently
// turn each query into a full rebuild.
func TestStampGen_IsDeterministic(t *testing.T) {
	const ddl = "CREATE TABLE x(a INT);"
	tests := []struct {
		name string
		a, b []string
	}{
		{"identical input", []string{".go", ".ts"}, []string{".go", ".ts"}},
		{"extension order", []string{".go", ".ts"}, []string{".ts", ".go"}},
		{"empty set", nil, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, want := stampGen(ddl, tt.a), stampGen(ddl, tt.b)
			if got != want {
				t.Errorf("stampGen(%v) = %q, stampGen(%v) = %q; want equal\n"+
					"a generation that is not stable rebuilds every graph on every query",
					tt.a, got, tt.b, want)
			}
		})
	}
}

// The live stamp must carry the derived generation, not a hand-written
// literal — a literal is what goes stale.
func TestStamp_CarriesDerivedGeneration(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module x\n")
	writeFile(t, repo, "a.go", "package x\n")

	got, err := stamp(repo)
	if err != nil {
		t.Fatalf("stamp: %v", err)
	}
	want := stampGen(schema, indexedExtensions()) + ":"
	if !strings.HasPrefix(got, want) {
		t.Errorf("stamp = %q, want prefix %q", got, want)
	}
}

// Build output is not source. Walking it makes the stamp move on every build
// and, once the mapper indexes more than .go, would index generated trees.
func TestStamp_SkipsBuildOutputDirs(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module x\n")
	writeFile(t, repo, "a.go", "package x\n")
	before, err := stamp(repo)
	if err != nil {
		t.Fatalf("stamp: %v", err)
	}

	for _, dir := range []string{"dist", "build", "target", "__pycache__", ".venv"} {
		writeFile(t, filepath.Join(repo, dir), "generated.go", "package gen\n")
	}
	after, err := stamp(repo)
	if err != nil {
		t.Fatalf("stamp: %v", err)
	}
	if before != after {
		t.Errorf("stamp moved after writing build output:\n before %s\n after  %s", before, after)
	}
}

// canonicalRepo keys the on-disk cache. moduleRoot anchors on go.mod, which a
// non-Go repo does not have, so a subdirectory keys a SECOND cache — the exact
// split-cache bug moduleRoot was introduced to close for Go. A checkout is a
// repository whether or not it holds a go.mod, so .git is the language-neutral
// anchor.
func TestCanonicalRepo_NonGoRepoAnchorsAtGitRoot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git"), "HEAD", "ref: refs/heads/main\n")
	writeFile(t, root, "package.json", `{"name":"x"}`)
	sub := filepath.Join(root, "packages", "web")
	writeFile(t, sub, "index.ts", "export const a = 1;\n")

	wantRoot, err := canonicalRepo(root)
	if err != nil {
		t.Fatalf("canonicalRepo(root): %v", err)
	}
	got, err := canonicalRepo(sub)
	if err != nil {
		t.Fatalf("canonicalRepo(sub): %v", err)
	}
	if got != wantRoot {
		t.Errorf("canonicalRepo(%q) = %q, want the git root %q", sub, got, wantRoot)
	}
}

// go.mod still wins over .git: a nested module inside a larger checkout is its
// own indexing scope, which is what packages.Load(Dir:repo, "./...") covers.
func TestCanonicalRepo_ModuleRootBeatsGitRoot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git"), "HEAD", "ref: refs/heads/main\n")
	mod := filepath.Join(root, "services", "api")
	writeFile(t, mod, "go.mod", "module example.com/api\n")
	sub := filepath.Join(mod, "internal", "handler")
	writeFile(t, sub, "h.go", "package handler\n")

	got, err := canonicalRepo(sub)
	if err != nil {
		t.Fatalf("canonicalRepo: %v", err)
	}
	want, err := canonicalRepo(mod)
	if err != nil {
		t.Fatalf("canonicalRepo(mod): %v", err)
	}
	if got != want {
		t.Errorf("canonicalRepo(%q) = %q, want the nested module root %q", sub, got, want)
	}
}
