// Mapper-tier import extraction -> importRow (the `imports` table), by two
// paths that meet at the same row shape. Python goes through
// gts.ExtractImports, which has import-node support for go/java/python/
// starlark — of those, only python is a mapper language (mapper.go's
// mapperLanguages). The JS family (typescript/tsx/javascript) has no such
// support upstream, so it goes through tsImportsQuery below.
//
// Every row this file writes carries the module SPECIFIER exactly as written
// in source; resolving one to a repo file is mapper_calls.go's job, next to
// the ladder rung that consumes it.
//
// Alongside the rows, the JS family also yields BINDINGS: the local names an
// import introduces in the importing file. Those never persist — the imports
// table has no column for them — they exist only to feed ladder rung 2a
// within the same build.
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

// jsFamilyLanguages is the subset of mapperLanguages whose imports come from
// tsImportsQuery rather than gts.ExtractImports. Python is the complement.
var jsFamilyLanguages = map[string]bool{
	"typescript": true,
	"tsx":        true,
	"javascript": true,
}

// tsImportsQuery closes the gap gotreesitter leaves for the JS family: it
// emits FactSet.Imports for go and python but zero for TypeScript, and
// upstream tree-sitter-typescript's tags.scm carries no import patterns
// either. A .scm query is data, so this costs no CGO — the constraint that
// rules out the official C bindings and any C++-side wrapper alike.
//
// Covers every import form that names a module: static (default, named,
// namespace, type-only, side-effect), re-export, dynamic import(), and
// CommonJS require(). The captured @name is the module specifier, which is
// what an import edge points at.
//
// The last pattern's #eq? predicate is what keeps `require` from widening
// into "any single-string call": without it every f("...") in the tree would
// be reported as an import.
const tsImportsQuery = `
(import_statement
  source: (string (string_fragment) @name)) @reference.import

(export_statement
  source: (string (string_fragment) @name)) @reference.import

(call_expression
  function: (import)
  arguments: (arguments (string (string_fragment) @name))) @reference.import

(call_expression
  function: (identifier) @_fn
  arguments: (arguments (string (string_fragment) @name))) @reference.import
(#eq? @_fn "require")
`

// tsBindingsQuery is a SECOND query over the same tree, kept separate from
// tsImportsQuery rather than folded into it: that one is validated against
// real corpora for specifier recall, and adding captures to its patterns
// would change what each match carries. Each pattern here binds one local
// name to the specifier it came from, in the same match.
//
// The `!alias` anchor on the last pattern is load-bearing. Without it,
// `import { Foo as Bar }` records BOTH Bar (via the alias pattern) and Foo
// (via this one) — but Foo is not in scope in the importing file at all.
// Rung 2a would then fire on a receiver named Foo that means something else,
// narrow to the wrong file, and STOP the ladder: a missed caller, which the
// over-approximation contract does not permit.
//
// A side-effect import (`import "./x"`) binds nothing and matches no pattern
// here by construction — it has no import_clause.
const tsBindingsQuery = `
(import_statement
  (import_clause (identifier) @binding)
  source: (string (string_fragment) @name)) @binding.default

(import_statement
  (import_clause (namespace_import (identifier) @binding))
  source: (string (string_fragment) @name)) @binding.namespace

(import_statement
  (import_clause (named_imports (import_specifier alias: (identifier) @binding)))
  source: (string (string_fragment) @name)) @binding.alias

(import_statement
  (import_clause (named_imports (import_specifier !alias name: (identifier) @binding)))
  source: (string (string_fragment) @name)) @binding.named
`

// mapperImportBindings maps a repo-relative importer file to the local names
// its imports introduce, each pointing at the module specifier it came from:
// file -> binding name -> specifier. Build-time only, never persisted.
type mapperImportBindings map[string]map[string]string

// mapperImports parses every Python and JS-family file in files an EXTRA
// time (mirroring mapperSymbols/collectMapperCalls/mapperCarry's own
// established policy: gts.Parser is not safe to share across passes, and no
// prior pass returns a tree) and extracts its import declarations — via
// gts.ExtractImports for Python, via tsImportsQuery for the JS family.
// Per-file failures (unreadable file, unparseable source) are
// skip-and-continue, mirroring mapperSymbols/collectMapperCalls' own policy
// exactly.
func mapperImports(files []mapperFile) ([]importRow, mapperImportBindings, mapperStats) {
	var stats mapperStats
	engines := mapperEngines{}
	var out []importRow
	bindings := mapperImportBindings{}

	for _, f := range files {
		if f.entry == nil {
			continue
		}
		isPython := f.entry.Name == "python"
		if !isPython && !jsFamilyLanguages[f.entry.Name] {
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

		if !isPython {
			if eng.imports == nil {
				stats.parseErr++ // the query failed to compile for this grammar
				continue
			}
			for _, m := range eng.imports.Execute(tree) {
				for _, c := range m.Captures {
					if c.Name != "name" {
						continue // @_fn is the require predicate's operand, not a module
					}
					if spec := c.Text(src); spec != "" {
						out = append(out, importRow{
							importerFile:   f.rel,
							importedModule: spec,
							precision:      mapperImportPrecision,
						})
					}
				}
			}
			if eng.bindings != nil {
				for _, m := range eng.bindings.Execute(tree) {
					var name, spec string
					for _, c := range m.Captures {
						switch c.Name {
						case "binding":
							name = c.Text(src)
						case "name":
							spec = c.Text(src)
						}
					}
					if name == "" || spec == "" {
						continue
					}
					if bindings[f.rel] == nil {
						bindings[f.rel] = map[string]string{}
					}
					bindings[f.rel][name] = spec
				}
			}
			continue
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
	return out, bindings, stats
}
