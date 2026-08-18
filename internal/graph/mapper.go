// Mapper-tier discovery: walk a repo and decide, per file, whether it feeds
// the mapper conversion stage (mapper_symbols.go). This file makes no parse
// decision — that lives entirely on the conversion side (design.md decision
// 2), so the discovery/conversion split is a FILE boundary, not a diff cut.
//
// Nothing calls mapperFiles yet outside its own test and engine.go's pin
// removal (Phase 3) — this slice produces no symbol rows, writes nothing to
// graph.db, and shares no lookup structure with the Go semantic tier.
package graph

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"github.com/odvcencio/gotreesitter/grammars"
)

// mapperLanguages restricts mapper-tier processing to the four grammars this
// slice supports. Go is deliberately excluded: index.go's AST-based semantic
// tier already owns it.
var mapperLanguages = map[string]bool{
	"typescript": true,
	"tsx":        true,
	"javascript": true,
	"python":     true,
}

// maxMapperFileBytes bounds a single mapper-tier file. The conversion stage
// reads each discovered file whole and hands it to a parser that builds a
// tree over it, so an unbounded read is an availability problem: droids-mem
// runs as a long-lived MCP server and Manager builds repos concurrently, so
// one pathological file costs every session, not just this build. The
// mapper covers .js, where multi-MB generated and minified bundles are
// routine and live outside skipDir's set (public/, static/, assets/, a
// stray *.min.js under src/). 2 MiB is far above hand-written source and far
// below a bundle. The cap is inclusive: a file exactly at it is kept.
const maxMapperFileBytes = 2 << 20

// mapperFile is the discovery→conversion seam: everything the walk decides,
// nothing the parser decides.
type mapperFile struct {
	abs        string              // absolute path, for os.ReadFile
	rel        string              // repo-relative, slash-separated → symRow.file
	entry      *grammars.LangEntry // detected once; yields Language() and the tags query
	modulePath string              // → symRow.pkg, always non-empty
}

// mapperStats makes discovery's skip-and-continue policy observable, per run
// (never global — tests need no reset, and Manager builds different repos
// concurrently).
type mapperStats struct {
	walked           int
	skippedLang      int
	skippedTest      int
	skippedIrregular int
	skippedSize      int
	readErr          int
	parseErr         int
	outlineDecline   int
}

// mapperFiles walks repo, reusing stamp.go's existing skipDir policy (no
// second directory-skip list), detects each file's language, restricts to
// mapperLanguages, excludes mapper-tier test files, and synthesizes each
// kept file's modulePath. It returns an error only for a WalkDir failure at
// the root; every per-entry problem is skip-and-continue and counted in the
// returned mapperStats instead.
func mapperFiles(repo string) ([]mapperFile, mapperStats, error) {
	var stats mapperStats
	var files []mapperFile
	repoBase := filepath.Base(repo)

	err := filepath.WalkDir(repo, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			stats.readErr++
			return nil //nolint:nilerr // an unreadable entry (e.g. a ReadDir failure on a permission-denied dir) is skip-and-continue, not fatal
		}
		if d.IsDir() {
			if p != repo && skipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		// Drop every non-regular entry. os.ReadFile in the conversion stage
		// follows symlinks, so a symlinked source file would pull content
		// from anywhere the user can read and store it under a rel/pkg that
		// misattributes where it came from — and a hostile repo can ship one,
		// because git preserves symlinks through a clone. Symlinked
		// DIRECTORIES need no separate handling: filepath.WalkDir reports
		// them with IsDir()==false and never descends.
		if !d.Type().IsRegular() {
			stats.skippedIrregular++
			return nil
		}
		info, err := d.Info()
		if err != nil {
			stats.readErr++
			return nil //nolint:nilerr // a racing delete/permission change on a single file doesn't abort the walk
		}
		stats.walked++

		if info.Size() > maxMapperFileBytes {
			stats.skippedSize++
			return nil
		}

		entry := grammars.DetectLanguage(d.Name())
		if entry == nil || !mapperLanguages[entry.Name] {
			stats.skippedLang++
			return nil
		}

		rel, err := filepath.Rel(repo, p)
		if err != nil {
			rel = p
		}
		rel = filepath.ToSlash(rel)

		if isMapperTestFile(rel, entry.Name) {
			stats.skippedTest++
			return nil
		}

		files = append(files, mapperFile{
			abs:        p,
			rel:        rel,
			entry:      entry,
			modulePath: modulePath(rel, entry.Name, repoBase),
		})
		return nil
	})
	if err != nil {
		return nil, stats, fmt.Errorf("walk mapper files in %s: %w", repo, err)
	}
	return files, stats, nil
}

// isMapperTestFile reports whether rel is a test file for the MAPPER TIER
// only — the Go semantic tier's Tests:true handling (index.go) is untouched.
// Patterns are derived per language from that language's own test runner's
// discovery spec (see design.md's provenance table: pytest python_files +
// conftest.py; jest's two default testMatch patterns, which are a strict
// superset of vitest's). The bare test/tests/spec directory-segment rule
// below is convention, not runner-derived — retained from the spec's
// measured set (axios fan-out 4.24x→1.42x, zustand qname collisions 32→2).
func isMapperTestFile(rel, lang string) bool {
	rel = filepath.ToSlash(rel)
	segments := strings.Split(rel, "/")
	base := segments[len(segments)-1]
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	dirs := segments[:len(segments)-1]

	for _, seg := range dirs {
		if seg == "test" || seg == "tests" || seg == "spec" {
			return true
		}
	}

	switch lang {
	case "python":
		if base == "conftest.py" {
			return true
		}
		return strings.HasPrefix(stem, "test_") || strings.HasSuffix(stem, "_test")
	case "typescript", "tsx", "javascript":
		if slices.Contains(dirs, "__tests__") {
			return true
		}
		if stem == "test" || stem == "spec" {
			return true
		}
		return strings.HasSuffix(stem, ".test") || strings.HasSuffix(stem, ".spec")
	default:
		return false
	}
}

// modulePath synthesizes the file's package/module identity: for TS/TSX/JS,
// the extensionless slash-separated path; for Python, the dotted package
// path with a trailing "__init__" segment stripped. repoBase is
// filepath.Base(repo), used only as the fallback for a root-level
// __init__.py, whose dotted path would otherwise strip to empty —
// symbols.package is NOT NULL and spec-required non-empty, so this fallback
// keeps every mapper symbol's pkg non-empty.
func modulePath(rel, lang, repoBase string) string {
	rel = filepath.ToSlash(rel)
	ext := filepath.Ext(rel)
	stem := strings.TrimSuffix(rel, ext)
	if lang != "python" {
		return stem
	}

	dotted := strings.ReplaceAll(stem, "/", ".")
	if dotted == "__init__" {
		return repoBase // repo-root __init__.py: no package prefix to strip
	}
	if trimmed, ok := strings.CutSuffix(dotted, ".__init__"); ok {
		return trimmed
	}
	return dotted
}
