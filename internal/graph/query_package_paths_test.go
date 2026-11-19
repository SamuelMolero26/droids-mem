package graph

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPackageExplicitPaths(t *testing.T) {
	for _, tc := range []struct {
		ext, file, decl, rootKey string
	}{
		{".go", "worker", "package %s\nfunc %s() {}\n", "evidenceunit"},
		{".py", "__init__", "def %[2]s():\n    pass\n", "checkout-fixture"},
		{".js", "worker", "export function %[2]s() {}\n", "worker"},
		{".jsx", "worker", "export function %[2]s() { return <span /> }\n", "worker"},
		{".ts", "worker", "export function %[2]s() {}\n", "worker"},
		{".tsx", "worker", "export function %[2]s() { return <span /> }\n", "worker"},
		{".swift", "worker", "public func %[2]s() {}\n", "worker"},
	} {
		t.Run(strings.TrimPrefix(tc.ext, "."), func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "checkout-fixture")
			if tc.ext == ".go" {
				writeFile(t, repo, "go.mod", "module example.com/evidenceunit\n\ngo 1.21\n")
			}
			for _, f := range []struct{ dir, pkg, name string }{
				{".", "different", "Root"},
				{"nested", "different", "Nested"},
				{"other/nested", "nested", "Other"},
				{"other/missing", "missing", "Decoy"},
				{"tree/leaf", "leaf", "Leaf"},
			} {
				writeFile(t, filepath.Join(repo, f.dir), tc.file+tc.ext, fmt.Sprintf(tc.decl, f.pkg, f.name))
			}
			if tc.ext == ".py" {
				// nested.py and nested/__init__.py share a key, but only the
				// latter belongs to the explicit nested directory.
				writeFile(t, repo, "nested.py", "def RootPeer():\n    pass\n")
			}
			m := NewManager(t.TempDir())
			t.Cleanup(m.Close)
			if _, err := m.Index(t.Context(), repo); err != nil {
				t.Fatal(err)
			}
			query := func(name string) (*PackageResponse, error) {
				return m.Package(t.Context(), PackageRequest{Repo: repo, Package: name})
			}
			root, err := query(tc.rootKey)
			if err != nil || len(root.Symbols) != 1 {
				t.Fatalf("explicit root %q: %+v, %v", tc.rootKey, root, err)
			}
			t.Run("root signatures and areas", func(t *testing.T) {
				got, err := query(".")
				if err != nil {
					t.Fatal(err)
				}
				if tc.ext == ".py" {
					if len(got.Symbols) != 2 || !slices.Contains(got.Symbols, root.Symbols[0]) {
						t.Errorf("root symbols = %+v; want Root and RootPeer", got.Symbols)
					}
				} else if !slices.Equal(got.Symbols, root.Symbols) {
					t.Errorf("root symbols = %+v; explicit package = %+v", got.Symbols, root.Symbols)
				}
				if !strings.Contains(RenderPackage(got), "./nested,1") {
					t.Errorf("root did not show ./nested with one indexed file:\n%s", RenderPackage(got))
				}
			})
			for _, dir := range []string{"nested", "tree"} {
				t.Run(dir, func(t *testing.T) {
					got, err := query("./" + dir + "/")
					if err != nil {
						t.Fatal(err)
					}
					if len(got.Symbols) != 1 || !strings.HasPrefix(got.Symbols[0].File, dir+"/") {
						t.Errorf("symbols for %q = %+v; want only the symbol under that directory", dir, got.Symbols)
					}
				})
			}
			t.Run("missing never falls back to suffix", func(t *testing.T) {
				if _, err := query("missing"); err != nil {
					t.Fatalf("legacy suffix did not find decoy: %v", err)
				}
				if _, err := query("./missing"); !errors.Is(err, ErrNotFound) {
					t.Errorf("missing directory error = %v; want ErrNotFound", err)
				}
			})
		})
	}
}

func TestPackageRootOverviewWithoutRootFiles(t *testing.T) {
	repo := t.TempDir()
	for i := range 16 {
		writeFile(t, filepath.Join(repo, fmt.Sprintf("area%02d", i)), "worker.js", "export function Work() {}\n")
	}
	m := NewManager(t.TempDir())
	t.Cleanup(m.Close)
	got, err := m.Package(t.Context(), PackageRequest{Repo: repo, Package: "."})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Symbols) != 0 {
		t.Errorf("root overview listed descendant symbols: %+v", got.Symbols)
	}
	text := RenderPackage(got)
	for _, want := range []string{"./area00,1", "areas_total: 16"} {
		if !strings.Contains(text, want) {
			t.Errorf("root overview missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "./area15") {
		t.Errorf("root overview did not cap area rows:\n%s", text)
	}
}
