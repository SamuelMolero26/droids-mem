package graph

import (
	"os"
	"path/filepath"
	"testing"
)

// canonicalRepo keys the on-disk graph cache. If it accepts a subdirectory as
// given, an agent passing repo=/repo/cmd gets a SECOND cache built from only
// that subtree's package set, while meta.module still names the whole module —
// a graph that under-reports callers and transitive_callers without saying so.
// Normalising to the module root collapses every path inside one module onto
// one correct cache.
func TestCanonicalRepo_NormalisesToModuleRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/m\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "cmd", "tool")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}

	wantRoot, err := canonicalRepo(root)
	if err != nil {
		t.Fatalf("canonicalRepo(root): %v", err)
	}

	got, err := canonicalRepo(sub)
	if err != nil {
		t.Fatalf("canonicalRepo(sub): %v", err)
	}
	if got != wantRoot {
		t.Errorf("canonicalRepo(%q) = %q, want the module root %q", sub, got, wantRoot)
	}
}

// A directory that is itself a module must stay put — walking up must stop at
// the FIRST go.mod, or a nested module would be indexed as its parent and the
// two would collide on one cache key.
func TestCanonicalRepo_NestedModuleStaysPut(t *testing.T) {
	outer := t.TempDir()
	if err := os.WriteFile(filepath.Join(outer, "go.mod"), []byte("module example.com/outer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(outer, "inner")
	if err := os.MkdirAll(inner, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inner, "go.mod"), []byte("module example.com/inner\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := canonicalRepo(inner)
	if err != nil {
		t.Fatalf("canonicalRepo(inner): %v", err)
	}
	want, err := canonicalRepo(inner + "/.")
	if err != nil {
		t.Fatal(err)
	}
	if got != want || filepath.Base(got) != "inner" {
		t.Errorf("nested module resolved to %q, want the inner module root", got)
	}
}

// No go.mod anywhere up the chain (GOPATH-style tree, or a plain directory):
// the path is returned unchanged, preserving today's behaviour so buildIndex
// still reports its own "no Go packages found" rather than a confusing
// resolved-elsewhere path.
func TestCanonicalRepo_NoModuleReturnsPathUnchanged(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	got, err := canonicalRepo(sub)
	if err != nil {
		t.Fatalf("canonicalRepo: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(sub)
	if err != nil {
		t.Fatal(err)
	}
	if got != resolved {
		t.Errorf("canonicalRepo(%q) = %q, want it unchanged at %q", sub, got, resolved)
	}
}

// The real testdata module: every package inside it keys to the module root.
func TestCanonicalRepo_TestdataSubpackageKeysToRoot(t *testing.T) {
	root, err := canonicalRepo("testdata/testmod")
	if err != nil {
		t.Fatalf("canonicalRepo(testmod): %v", err)
	}
	got, err := canonicalRepo("testdata/testmod/zz")
	if err != nil {
		t.Fatalf("canonicalRepo(testmod/zz): %v", err)
	}
	if got != root {
		t.Errorf("zz subpackage keyed to %q, want module root %q", got, root)
	}
}
