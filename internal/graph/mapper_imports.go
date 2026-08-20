// Mapper-tier import extraction: gts.ExtractImports -> importRow (the
// `imports` table). Python only in this slice — gts.ExtractImports has
// import-node support for go/java/python/starlark, of which only python is a
// mapper language (mapper.go's mapperLanguages); TS/JS module-specifier
// capture and binding-name/resolution work is PR-G1/G2, deliberately not
// attempted here.
package graph

import (
	"os"

	gts "github.com/odvcencio/gotreesitter"
)

// importRow is one row destined for the `imports` table: an import edge
// whose endpoints are module TEXT, not symbol ids (graph.go's imports table
// comment) — most imported modules have no symbol row at all (e.g. `import
// foo.bar` where foo.bar is a third-party dependency, not repo source).
type importRow struct {
	importerFile   string
	importedModule string
	precision      string
}

// mapperImportPrecision is the value every row from this slice writes to
// imports.precision (the column has NO default, unlike edges.precision/
// implements.precision — every insert must set it explicitly or the insert
// fails). Import extraction here is purely syntactic: gts.ExtractImports
// reads the parsed tree's import syntax and never resolves a specifier
// against the filesystem or the repo's symbol table (that resolution is
// PR-G2, TS/JS-only), so every row this slice writes is "syntactic" —
// mirroring the mapper tier's edgeSet precision convention (index.go,
// mapper_calls.go) rather than inventing a third axis just for imports.
const mapperImportPrecision = precisionSyntactic

// mapperImports parses every Python file in files an EXTRA time (mirroring
// mapperSymbols/collectMapperCalls/mapperCarry's own established policy:
// gts.Parser is not safe to share across passes, and no prior pass returns a
// tree) and extracts import declarations via gts.ExtractImports. Only Python
// files are processed — files of any other mapper language are silently
// skipped, not counted as an error, since wiring them is explicitly out of
// scope for this slice (see the package doc comment above). Per-file
// failures (unreadable file, unparseable source) are skip-and-continue,
// mirroring mapperSymbols/collectMapperCalls' own policy exactly.
func mapperImports(files []mapperFile) ([]importRow, mapperStats) {
	var stats mapperStats
	engines := mapperEngines{}
	var out []importRow

	for _, f := range files {
		if f.entry == nil || f.entry.Name != "python" {
			continue
		}
		// #nosec G304 -- discovery admits only regular files under repo, size-capped
		// at maxMapperFileBytes; symlinks are dropped there so this read cannot
		// resolve outside the indexed repo.
		src, err := os.ReadFile(f.abs)
		if err != nil {
			stats.readErr++
			continue // unreadable file is skip-and-continue, not fatal
		}

		eng := engines.get(f.entry)
		if eng.lang == nil {
			stats.parseErr++ // no working language: the grammar failed to load
			continue
		}

		parser := gts.NewParser(eng.lang) // fresh per file: Parser is not concurrency-safe
		tree, err := parser.Parse(src)
		if err != nil {
			stats.parseErr++
			continue // unparsable file is skip-and-continue, not fatal
		}

		for _, ref := range gts.ExtractImports(tree) {
			// Kind is "import" or "from_import" for python (gts's own
			// extractPythonImportNode never emits "package" — that Kind exists
			// only for go/java's own package clause). Path is already the full
			// dotted module text ("foo.bar" for `import foo.bar`; "foo.bar" for
			// `from foo import bar` via joinPythonImportPath) — no assembly
			// needed here.
			if ref.Kind != "import" && ref.Kind != "from_import" {
				continue
			}
			if ref.Path == "" {
				continue // defensive: never write an empty imported_module
			}
			out = append(out, importRow{
				importerFile:   f.rel,
				importedModule: ref.Path,
				precision:      mapperImportPrecision,
			})
		}
	}
	return out, stats
}
