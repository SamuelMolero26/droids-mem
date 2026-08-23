package graph

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// TestPackage_JSDirAggregates pins the JS/TS mapper tier's directory-level
// package semantics: a directory like "app/components/ui" has no symbol with
// package exactly that string — children live at
// "app/components/ui/button", "app/components/ui/input", etc. Querying the
// directory must aggregate all descendant modules, not answer not_found.
func TestPackage_JSDirAggregates(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "app/components/ui"), "button.tsx", "export function Button() {}\n")
	writeFile(t, filepath.Join(repo, "app/components/ui"), "input.tsx", "export function Input() {}\n")
	writeFile(t, filepath.Join(repo, "app/components/forms"), "form.tsx", "export function Form() {}\n")

	m := NewManager(filepath.Join(t.TempDir(), "graphs"))
	t.Cleanup(m.Close)
	ctx := context.Background()
	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatalf("Index: %v", err)
	}

	// Qualified directory.
	resp, err := m.Package(ctx, PackageRequest{Repo: repo, Package: "app/components/ui"})
	if err != nil {
		t.Fatalf("Package app/components/ui: %v", err)
	}
	if len(resp.Symbols) != 2 {
		t.Errorf("app/components/ui symbols = %d, want 2 (Button + Input)", len(resp.Symbols))
	}
	if resp.Package != "app/components/ui" {
		t.Errorf("Package = %q, want %q (directory, not a child)", resp.Package, "app/components/ui")
	}

	// Bare mid-path suffix: "components/ui" must also aggregate via infix.
	resp, err = m.Package(ctx, PackageRequest{Repo: repo, Package: "components/ui"})
	if err != nil {
		t.Fatalf("Package components/ui: %v", err)
	}
	if len(resp.Symbols) != 2 {
		t.Errorf("components/ui symbols = %d, want 2", len(resp.Symbols))
	}

	// Parent directory aggregates its subtree (ui/* plus forms/*).
	resp, err = m.Package(ctx, PackageRequest{Repo: repo, Package: "app/components"})
	if err != nil {
		t.Fatalf("Package app/components: %v", err)
	}
	if len(resp.Symbols) != 3 {
		t.Errorf("app/components symbols = %d, want 3", len(resp.Symbols))
	}
}

// TestPackage_JSLeafSuffix verifies bare leaf suffix still works on the JS/TS
// tier: querying "button" (no slash) must find "app/components/ui/button"
// via the slash-anchored suffix rung.
func TestPackage_JSLeafSuffix(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "app/components/ui"), "button.tsx", "export function Button() {}\n")
	writeFile(t, filepath.Join(repo, "app/components/ui"), "input.tsx", "export function Input() {}\n")

	m := NewManager(filepath.Join(t.TempDir(), "graphs"))
	t.Cleanup(m.Close)
	ctx := context.Background()
	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatalf("Index: %v", err)
	}

	// Bare leaf.
	resp, err := m.Package(ctx, PackageRequest{Repo: repo, Package: "button"})
	if err != nil {
		t.Fatalf("Package button: %v", err)
	}
	if len(resp.Symbols) != 1 || resp.Symbols[0].QName != "app/components/ui/button:Button" {
		t.Errorf("button symbols = %+v, want exactly app/components/ui/button:Button", resp.Symbols)
	}

	// Bare directory leaf "ui" must aggregate its children, not resolve to a
	// single file via suffix (there is no file-level "ui" module).
	resp, err = m.Package(ctx, PackageRequest{Repo: repo, Package: "ui"})
	if err != nil {
		t.Fatalf("Package ui: %v", err)
	}
	if len(resp.Symbols) != 2 {
		t.Errorf("ui symbols = %d, want 2 (aggregated children)", len(resp.Symbols))
	}
}

// TestPackage_RepoValidation pins that an empty repo is invalid_argument, not
// a silent CWD fallback that happens to contain a "Button" symbol.
func TestPackage_RepoValidation(t *testing.T) {
	m := NewManager(filepath.Join(t.TempDir(), "graphs"))
	t.Cleanup(m.Close)
	_, err := m.Package(context.Background(), PackageRequest{Repo: "", Package: "ui"})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("Package with empty repo = %v, want ErrInvalidArgument", err)
	}
	_, err = m.Package(context.Background(), PackageRequest{Repo: "   ", Package: "ui"})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("Package with whitespace repo = %v, want ErrInvalidArgument", err)
	}
	_, err = m.Package(context.Background(), PackageRequest{Repo: "/tmp", Package: ""})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("Package with empty package = %v, want ErrInvalidArgument", err)
	}
	_, err = m.Package(context.Background(), PackageRequest{Repo: "/tmp", Package: "   "})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("Package with whitespace package = %v, want ErrInvalidArgument", err)
	}
}

func TestSymbol_RepoValidation(t *testing.T) {
	m := NewManager(filepath.Join(t.TempDir(), "graphs"))
	t.Cleanup(m.Close)
	_, err := m.Symbol(context.Background(), SymbolRequest{Repo: "", Symbol: "Foo"})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("Symbol with empty repo = %v, want ErrInvalidArgument", err)
	}
	_, err = m.Symbol(context.Background(), SymbolRequest{Repo: "/tmp", Symbol: ""})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("Symbol with empty symbol = %v, want ErrInvalidArgument", err)
	}
	_, err = m.Symbol(context.Background(), SymbolRequest{Repo: "   ", Symbol: "Foo"})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("Symbol with whitespace repo = %v, want ErrInvalidArgument", err)
	}
}

// TestPackage_MapperForms re-pins the Python dotted forms after the JS dir
// work, so a slash-path query like "app/models/scorer" still finds the dotted
// module "app.models.scorer".
func TestPackage_MapperForms(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "app/models"), "persona.py", "class UserPersonaModel:\n    def dominant_persona(self):\n        return 0\n")
	writeFile(t, filepath.Join(repo, "app/models"), "scorer.py", "def recommend_diverse(items):\n    return items\n")
	m := NewManager(filepath.Join(t.TempDir(), "graphs"))
	t.Cleanup(m.Close)
	ctx := context.Background()
	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatalf("Index: %v", err)
	}
	const want = "app.models.scorer"
	for _, form := range []string{"app.models.scorer", "scorer", "app/models/scorer", "models.scorer"} {
		resp, err := m.Package(ctx, PackageRequest{Repo: repo, Package: form})
		if err != nil {
			t.Fatalf("Package(%q): %v", form, err)
		}
		if resp.Package != want {
			t.Errorf("Package(%q) = %q, want %q", form, resp.Package, want)
		}
	}
	// Directory aggregation for Python.
	resp, err := m.Package(ctx, PackageRequest{Repo: repo, Package: "app.models"})
	if err != nil {
		t.Fatalf("Package app.models (dir): %v", err)
	}
	if len(resp.Symbols) < 2 {
		t.Errorf("app.models dir symbols = %d, want >=2 (persona + scorer)", len(resp.Symbols))
	}
}
