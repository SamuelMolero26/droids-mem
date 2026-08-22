package graph

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/packages"

	_ "modernc.org/sqlite"
)

// goFreeLoadConfig mirrors buildIndex's packages.Config exactly, so the
// characterization test below observes the SAME packages.Load behavior
// buildIndex itself will see.
func goFreeLoadConfig(repo string) *packages.Config {
	return &packages.Config{
		Context: context.Background(),
		Dir:     repo,
		Env:     goToolEnv(),
		Tests:   true,
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes |
			packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedModule,
	}
}

// goFreeRepo builds a fresh temp directory containing only a mapper-language
// file — no go.mod, no .go file at all.
func goFreeRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	writeFile(t, repo, "app.ts", "export function hello(): string { return 'hi'; }\n")
	return repo
}

// TestPackagesLoad_GoFreeTree_ObservedShape is task B.1: characterization,
// run FIRST and against the REAL packages.Load (no mock), per the task
// brief's requirement that the actual shape be established empirically
// before usableGoPackages (B.2) is written.
//
// Empirically observed (recorded here as the ground truth the rest of PR-B is
// built against):
//
//  1. A tree with NEITHER a go.mod NOR any .go file: packages.Load returns
//     err == nil and a single SYNTHETIC package — ID "./...", PkgPath
//     "./...", zero GoFiles/CompiledGoFiles, Module == nil, and one
//     ListError-kind entry in Errors ("directory prefix . does not contain
//     main module or its selected dependencies").
//  2. A tree WITH a go.mod but ZERO .go files: packages.Load returns
//     err == nil and an EMPTY pkgs slice (len == 0) — a second, distinct
//     shape from (1).
//
// Neither case produced a non-nil err from packages.Load itself (a missing
// Go toolchain, which WOULD produce one, was not exercised here). Design D2
// folds all three possible shapes — err != nil, empty slice, synthetic
// errored package — into a single "unusable" outcome, so usableGoPackages
// (B.2) below is written to normalize all three, not just the two observed.
func TestPackagesLoad_GoFreeTree_ObservedShape(t *testing.T) {
	t.Run("no go.mod at all", func(t *testing.T) {
		repo := goFreeRepo(t)
		pkgs, err := packages.Load(goFreeLoadConfig(repo), "./...")
		if err != nil {
			t.Fatalf("packages.Load returned an error, contradicting the pinned observation: %v", err)
		}
		if len(pkgs) != 1 {
			t.Fatalf("len(pkgs) = %d, want 1 (pinned synthetic-package observation)", len(pkgs))
		}
		if len(pkgs[0].Errors) == 0 {
			t.Fatal("synthetic package carries no Errors — pinned observation says it must")
		}
		if len(pkgs[0].GoFiles) != 0 || len(pkgs[0].CompiledGoFiles) != 0 {
			t.Errorf("synthetic package carries GoFiles/CompiledGoFiles, want none: %+v", pkgs[0])
		}
		if pkgs[0].Module != nil {
			t.Errorf("synthetic package carries a Module, want nil: %+v", pkgs[0].Module)
		}
	})

	t.Run("go.mod present, zero .go files", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, repo, "go.mod", "module x\n\ngo 1.21\n")
		writeFile(t, repo, "app.ts", "export const x = 1;\n")
		pkgs, err := packages.Load(goFreeLoadConfig(repo), "./...")
		if err != nil {
			t.Fatalf("packages.Load returned an error, contradicting the pinned observation: %v", err)
		}
		if len(pkgs) != 0 {
			t.Fatalf("len(pkgs) = %d, want 0 (pinned empty-slice observation)", len(pkgs))
		}
	})
}

// TestUsableGoPackages is task B.2: the funnel must normalize every shape
// pinned by B.1 above (plus a genuine load error, per design D2) to nil, and
// must pass through a normal, healthy package list unchanged.
func TestUsableGoPackages(t *testing.T) {
	t.Run("load error yields nil", func(t *testing.T) {
		if got := usableGoPackages([]*packages.Package{{ID: "x"}}, errFakeLoad); got != nil {
			t.Errorf("usableGoPackages with a load error = %v, want nil", got)
		}
	})

	t.Run("empty slice yields nil", func(t *testing.T) {
		if got := usableGoPackages(nil, nil); got != nil {
			t.Errorf("usableGoPackages(nil, nil) = %v, want nil", got)
		}
	})

	t.Run("synthetic no-Go-files package is dropped, yielding nil", func(t *testing.T) {
		synth := &packages.Package{
			ID: "./...", PkgPath: "./...",
			Errors: []packages.Error{{Msg: "directory prefix . does not contain main module or its selected dependencies", Kind: packages.ListError}},
		}
		if got := usableGoPackages([]*packages.Package{synth}, nil); got != nil {
			t.Errorf("usableGoPackages with only a synthetic no-Go-files package = %v, want nil", got)
		}
	})

	t.Run("a real package with GoFiles survives even if it also errors", func(t *testing.T) {
		real := &packages.Package{
			ID: "example.com/x", PkgPath: "example.com/x",
			GoFiles: []string{"a.go"},
			Errors:  []packages.Error{{Msg: "undefined: foo", Kind: packages.TypeError}},
		}
		got := usableGoPackages([]*packages.Package{real}, nil)
		if len(got) != 1 || got[0] != real {
			t.Errorf("usableGoPackages dropped a real (broken but file-backed) package: %v", got)
		}
	})

	t.Run("a healthy package list passes through unchanged", func(t *testing.T) {
		healthy := &packages.Package{ID: "example.com/x", PkgPath: "example.com/x", GoFiles: []string{"a.go"}}
		got := usableGoPackages([]*packages.Package{healthy}, nil)
		if len(got) != 1 || got[0] != healthy {
			t.Errorf("usableGoPackages altered a healthy package list: %v", got)
		}
	})
}

// errFakeLoad is a sentinel error standing in for a genuine packages.Load
// failure (e.g. a missing Go toolchain), which was not reproducible in this
// sandbox (see TestPackagesLoad_GoFreeTree_ObservedShape's doc comment).
var errFakeLoad = errPkgLoadSentinel{}

type errPkgLoadSentinel struct{}

func (errPkgLoadSentinel) Error() string { return "fake packages.Load failure" }

// TestBuildIndex_GoFreeTree_DoesNotPanic is task B.3: buildIndex over a
// Go-free fixture tree must not panic or hard-error, guarding the pkgs[0]
// derefs the pre-PR-B code performed unconditionally for module, Fset, and
// ssa.NewProgram. Both fixtures carry a mapper-indexable function (not a bare
// `const x = 1`, which the outliner does not capture as a symbol at all) so
// that post-PR-C, the mapper tier is non-empty and C.10's both-tiers-empty
// hard error correctly does not fire here — that error's own dedicated case
// is TestBuildIndex_BothTiersEmpty_HardErrors (mapper_wiring_test.go).
func TestBuildIndex_GoFreeTree_DoesNotPanic(t *testing.T) {
	cases := []struct {
		name string
		make func(t *testing.T) string
	}{
		{"no go.mod at all", goFreeRepo},
		{"go.mod present, zero .go files", func(t *testing.T) string {
			repo := t.TempDir()
			writeFile(t, repo, "go.mod", "module x\n\ngo 1.21\n")
			writeFile(t, repo, "app.ts", "export function hello(): string { return 'hi'; }\n")
			return repo
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := tc.make(t)
			dbPath := filepath.Join(t.TempDir(), "graph.db")
			st, err := stamp(repo)
			if err != nil {
				t.Fatal(err)
			}
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("buildIndex panicked on a Go-free tree: %v", r)
					}
				}()
				if err := buildIndex(context.Background(), repo, dbPath, st); err != nil {
					t.Fatalf("buildIndex returned a hard error on a Go-free tree (must tolerate it, not fail): %v", err)
				}
			}()
		})
	}
}

// TestBuildIndex_GoFreeTree_EmptyReasonHint (PR-B, tasks B.7/B.8) was scoped
// by the spec explicitly to "the PR-B-to-PR-C window": once PR-C wires
// mapper discovery into buildIndex, a Go-free tree with a mapper-language
// file is no longer empty at all (it now yields mapper symbols). See
// TestBuildIndex_GoPackageWithZeroDecls_EmptyReasonHint (mapper_wiring_test.go)
// for the residual post-PR-C case, and TestBuildIndex_BothTiersEmpty_HardErrors
// (task C.10) for the case this test used to exercise, which now hard-errors
// instead of succeeding-with-a-hint.

// TestBuildIndex_NonEmptyGraph_NoEmptyReason pins the negative case: a
// healthy Go build must not carry the empty-graph hint.
func TestBuildIndex_NonEmptyGraph_NoEmptyReason(t *testing.T) {
	repo := copyFixture(t)
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

	var reason string
	if err := conn.QueryRow(`SELECT value FROM meta WHERE key = 'empty_reason'`).Scan(&reason); err != nil {
		t.Fatalf("read meta.empty_reason: %v", err)
	}
	if reason != "" {
		t.Errorf("meta.empty_reason = %q, want empty for a healthy non-empty build", reason)
	}
}

// TestBuildIndex_GoFreeTree_MajorityBrokenCapNotFalselyTriggered guards the
// reasoning behind moving the majority-broken cap onto goPkgs instead of the
// raw packages.Load result: the single synthetic "no Go files" package on a
// go.mod-less tree carries an Errors entry, so counting it as "broken" over
// the RAW pkgs slice would make the cap fire on every Go-free tree (1 broken
// of 1 pkgs == 100%), reintroducing exactly the hard failure B.3/B.4 removes.
func TestBuildIndex_GoFreeTree_MajorityBrokenCapNotFalselyTriggered(t *testing.T) {
	repo := goFreeRepo(t)
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	st, err := stamp(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := buildIndex(context.Background(), repo, dbPath, st); err != nil {
		t.Fatalf("buildIndex: %v (majority-broken cap must not fire on a Go-free tree)", err)
	}
}
