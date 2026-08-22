package graph

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestMapperFiles_SkipsSkipDirDirectories pins that discovery reuses
// stamp.go's existing skipDir policy instead of introducing a second
// directory-skip list: files under node_modules/.git/vendor must never be
// visited.
func TestMapperFiles_SkipsSkipDirDirectories(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "node_modules"), "excluded.ts", "export function excluded() {}\n")
	writeFile(t, filepath.Join(repo, ".git"), "excluded.py", "def excluded(): pass\n")
	writeFile(t, filepath.Join(repo, "vendor"), "excluded.js", "function excluded() {}\n")
	writeFile(t, filepath.Join(repo, "src"), "keep.ts", "export function keep() {}\n")

	files, _, err := mapperFiles(repo)
	if err != nil {
		t.Fatalf("mapperFiles: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1: %+v", len(files), files)
	}
	if got := filepath.ToSlash(files[0].rel); got != "src/keep.ts" {
		t.Errorf("rel = %q, want src/keep.ts", got)
	}
}

// TestMapperFiles_SkipsUnsupportedLanguage covers both halves of "Restricted
// Language Detection": a nil grammars.DetectLanguage result (unknown
// extension) and a non-nil result outside mapperLanguages (Go — handled by
// the separate semantic tier, deliberately excluded here). Neither may panic
// or produce rows.
func TestMapperFiles_SkipsUnsupportedLanguage(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "unknown.zzznotalang", "whatever\n")
	writeFile(t, repo, "a.go", "package main\nfunc main() {}\n")
	writeFile(t, repo, "keep.py", "def keep(): pass\n")

	files, stats, err := mapperFiles(repo)
	if err != nil {
		t.Fatalf("mapperFiles: %v", err)
	}
	if len(files) != 1 || files[0].rel != "keep.py" {
		t.Fatalf("got %+v, want exactly keep.py", files)
	}
	if stats.skippedLang < 2 {
		t.Errorf("skippedLang = %d, want >= 2 (unknown ext + go)", stats.skippedLang)
	}
}

// TestMapperFiles_UnreadableEntryCounted pins skip-and-continue: an entry
// the walk cannot read must not abort discovery of the remaining files, and
// must be observable via mapperStats. A directory made unreadable makes
// filepath.WalkDir invoke the callback a second time with a non-nil error
// for that path (documented WalkDir behavior on a ReadDir failure).
func TestMapperFiles_UnreadableEntryCounted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based unreadable dir is not portable to windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permission bits")
	}
	repo := t.TempDir()
	blocked := filepath.Join(repo, "blocked")
	writeFile(t, blocked, "hidden.ts", "export function hidden() {}\n")
	writeFile(t, repo, "keep.ts", "export function keep() {}\n")

	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })

	files, stats, err := mapperFiles(repo)
	if err != nil {
		t.Fatalf("mapperFiles: %v, want nil (skip-and-continue)", err)
	}
	if stats.readErr < 1 {
		t.Errorf("readErr = %d, want >= 1 for the unreadable directory", stats.readErr)
	}
	found := false
	for _, f := range files {
		if f.rel == "keep.ts" {
			found = true
		}
	}
	if !found {
		t.Errorf("keep.ts missing from %+v — walk must continue past the unreadable entry", files)
	}
}

func TestIsMapperTestFile(t *testing.T) {
	tests := []struct {
		rel  string
		lang string
		want bool
	}{
		// Python: pytest python_files default + reserved conftest.py.
		{"test_foo.py", "python", true},
		{"foo_test.py", "python", true},
		{"conftest.py", "python", true},
		{"pkg/test_bar.py", "python", true},
		{"pkg/conftest.py", "python", true},
		// TS/TSX/JS: jest testMatch (__tests__ segment, bare test/spec stem,
		// stem ending .test/.spec).
		{"src/__tests__/util.ts", "typescript", true},
		{"foo.test.ts", "typescript", true},
		{"foo.spec.tsx", "tsx", true},
		{"test.ts", "typescript", true},
		{"spec.js", "javascript", true},
		// Convention (all four languages): a bare test/tests/spec directory
		// segment. Not runner-derived — see design.md provenance table.
		{"tests/helpers.py", "python", true},
		{"spec/widget.js", "javascript", true},
		{"test/case.ts", "typescript", true},
		// Negative anchoring: substrings that merely CONTAIN "test" must
		// never match — an unanchored *test* rule would wrongly drop these.
		{"helpertest.ts", "typescript", false},
		{"latest.ts", "typescript", false},
		{"manifest.js", "javascript", false},
		{"fastest.py", "python", false},
		// Ordinary source files are never test files.
		{"src/util/helpers.ts", "typescript", false},
		{"pkg/sub/mod.py", "python", false},
	}
	for _, tt := range tests {
		t.Run(tt.rel+"/"+tt.lang, func(t *testing.T) {
			if got := isMapperTestFile(tt.rel, tt.lang); got != tt.want {
				t.Errorf("isMapperTestFile(%q, %q) = %v, want %v", tt.rel, tt.lang, got, tt.want)
			}
		})
	}
}

func TestModulePath(t *testing.T) {
	tests := []struct {
		name     string
		rel      string
		lang     string
		repoBase string
		want     string
	}{
		{"ts extensionless slash path", "src/util/helpers.ts", "typescript", "myrepo", "src/util/helpers"},
		{"js extensionless slash path", "index.js", "javascript", "myrepo", "index"},
		{"python init stripped", "pkg/sub/__init__.py", "python", "myrepo", "pkg.sub"},
		{"python non-init module", "pkg/sub/mod.py", "python", "myrepo", "pkg.sub.mod"},
		{"python root init falls back to repo base", "__init__.py", "python", "myrepo", "myrepo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := modulePath(tt.rel, tt.lang, tt.repoBase)
			if got != tt.want {
				t.Errorf("modulePath(%q, %q, %q) = %q, want %q", tt.rel, tt.lang, tt.repoBase, got, tt.want)
			}
			if got == "" {
				t.Errorf("modulePath must never be empty (symbols.package is NOT NULL)")
			}
		})
	}
}

// TestMapperFiles_SkipsSymlinkEscapingRepo pins the security boundary:
// os.ReadFile follows symlinks, so a symlinked source file inside the repo
// would otherwise pull in content from anywhere the user can read and store
// it under a repo-relative rel/pkg that misattributes where it came from.
// A hostile repo can ship such a link — git preserves symlinks through a
// clone. Discovery must drop every non-regular entry.
//
// The symlinked directory half also pins existing filepath.WalkDir behavior:
// WalkDir reports a symlinked dir with IsDir()==false and never descends, so
// there is no directory-traversal escape to close — only the file case.
func TestMapperFiles_SkipsSymlinkEscapingRepo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs elevation on Windows")
	}
	outside := t.TempDir()
	writeFile(t, outside, "outside_secret.py", "SECRET = \"sk-live-abc123\"\ndef leak():\n    return SECRET\n")

	repo := t.TempDir()
	writeFile(t, repo, "real.py", "def real():\n    pass\n")
	if err := os.Symlink(filepath.Join(outside, "outside_secret.py"), filepath.Join(repo, "notes.py")); err != nil {
		t.Fatalf("symlink file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, "escape_dir")); err != nil {
		t.Fatalf("symlink dir: %v", err)
	}

	files, stats, err := mapperFiles(repo)
	if err != nil {
		t.Fatalf("mapperFiles: %v", err)
	}
	for _, f := range files {
		if f.rel != "real.py" {
			t.Errorf("discovered %q (abs %q); a symlinked entry must never be indexed", f.rel, f.abs)
		}
	}
	if len(files) != 1 {
		t.Fatalf("got %d files, want exactly real.py", len(files))
	}
	if stats.skippedIrregular < 1 {
		t.Errorf("skippedIrregular = %d, want >= 1 — the skip must be observable", stats.skippedIrregular)
	}
}

// TestMapperFiles_SkipsOversizeFile pins the memory-exhaustion bound.
// mapperSymbols reads each discovered file whole and hands it to a parser
// that builds a tree over it; the mapper newly covers .js, where multi-MB
// generated and minified bundles are routine and live outside the skipDir
// set (public/, static/, assets/, a stray *.min.js under src/). droids-mem
// runs as a long-lived MCP server building repos concurrently, so one such
// file is an availability problem for every session, not just this build.
func TestMapperFiles_SkipsOversizeFile(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "bundle.min.js", strings.Repeat("x", maxMapperFileBytes+1))
	writeFile(t, repo, "atcap.js", strings.Repeat("y", maxMapperFileBytes))
	writeFile(t, repo, "small.ts", "export function keep() {}\n")

	files, stats, err := mapperFiles(repo)
	if err != nil {
		t.Fatalf("mapperFiles: %v", err)
	}
	for _, f := range files {
		if f.rel == "bundle.min.js" {
			t.Errorf("oversize file %q was discovered; it must be skipped", f.rel)
		}
	}
	if len(files) != 2 {
		t.Fatalf("got %d files, want 2 (atcap.js + small.ts): %+v", len(files), files)
	}
	if stats.skippedSize != 1 {
		t.Errorf("skippedSize = %d, want exactly 1 — the cap is inclusive, so atcap.js must be kept", stats.skippedSize)
	}
}
