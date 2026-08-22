// Mapper-tier import extraction tests (PR-F, tasks F.7/F.8): Python imports
// via gts.ExtractImports, wired into buildIndex with an explicit non-empty
// precision on every insert (imports.precision has no DDL default, unlike
// edges.precision/implements.precision).
package graph

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// mapMapperFile is defined in mapper_symbols_test.go and reused here.

// TestPythonImports_SimpleImportProducesRowWithExplicitPrecision pins F.7's
// unit-level shape: mapperImports on a single "import foo.bar" Python file
// yields exactly one importRow with the exact fields the spec's scenario
// names, and a non-empty precision (the column has no default — an empty
// insert would fail at the DB layer, but this asserts it directly at the
// production-code layer too).
func TestPythonImports_SimpleImportProducesRowWithExplicitPrecision(t *testing.T) {
	dir := t.TempDir()
	f := mapMapperFile(t, dir, "a.py", "import foo.bar\n", "a")

	rows, _, stats := mapperImports([]mapperFile{f})
	if stats.parseErr != 0 || stats.readErr != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1: %+v", len(rows), rows)
	}
	r := rows[0]
	if r.importerFile != f.rel {
		t.Errorf("importerFile = %q, want %q", r.importerFile, f.rel)
	}
	if r.importedModule != "foo.bar" {
		t.Errorf("importedModule = %q, want %q", r.importedModule, "foo.bar")
	}
	if r.precision == "" {
		t.Errorf("precision is empty, want a non-empty value (imports.precision has no default)")
	}
}

// TestTSImports_DefaultImportProducesSpecifierRow pins G1.1/G1.2's contract:
// a TS default import yields exactly one row in the imports table carrying
// the module SPECIFIER ("./y") and an explicit non-empty precision. Binding
// names (the local name "Baz" an import introduces), specifier-to-repo-file
// resolution, and ladder rung 2a are explicitly out of scope for this PR —
// see PR-G2 in the tasks artifact. This test only proves the specifier
// lands; it does not assert anything about a binding name because this
// slice never extracts one.
func TestTSImports_DefaultImportProducesSpecifierRow(t *testing.T) {
	dir := t.TempDir()
	f := mapMapperFile(t, dir, "a.ts", "import Baz from \"./y\";\n", "a")

	rows, _, stats := mapperImports([]mapperFile{f})
	if stats.parseErr != 0 || stats.readErr != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1: %+v", len(rows), rows)
	}
	r := rows[0]
	if r.importerFile != f.rel {
		t.Errorf("importerFile = %q, want %q", r.importerFile, f.rel)
	}
	if r.importedModule != "./y" {
		t.Errorf("importedModule = %q, want %q", r.importedModule, "./y")
	}
	if r.precision == "" {
		t.Errorf("precision is empty, want a non-empty value (imports.precision has no default)")
	}
}

// TestJSImports_RequireCallProducesSpecifierRow covers the CommonJS
// require() form (distinct query branch from the ES-module import_statement
// form above) for a plain .js file — proving the query's second reference
// kind and the javascript grammar (not just typescript) both wire through
// mapperImports.
func TestJSImports_RequireCallProducesSpecifierRow(t *testing.T) {
	dir := t.TempDir()
	f := mapMapperFile(t, dir, "a.js", "const cjs = require(\"./g\");\n", "a")

	rows, _, stats := mapperImports([]mapperFile{f})
	if stats.parseErr != 0 || stats.readErr != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1: %+v", len(rows), rows)
	}
	if rows[0].importedModule != "./g" {
		t.Errorf("importedModule = %q, want %q", rows[0].importedModule, "./g")
	}
}

// TestBuildIndex_TSImportLandsInImportsTable is G1's end-to-end wiring pin,
// mirroring TestBuildIndex_PythonImportLandsInImportsTable above: a real
// buildIndex run over a TS file containing an import must populate the
// imports table with a specifier row whose precision is non-empty.
func TestBuildIndex_TSImportLandsInImportsTable(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "a.ts", "import Baz from \"./y\";\n\nfunction use(): void {}\n")

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

	var importerFile, importedModule, precision string
	err = conn.QueryRow(`SELECT importer_file, imported_module, precision FROM imports
		WHERE imported_module = ?`, "./y").Scan(&importerFile, &importedModule, &precision)
	if err != nil {
		t.Fatalf("read imports row: %v", err)
	}
	if importedModule != "./y" {
		t.Errorf("imported_module = %q, want %q", importedModule, "./y")
	}
	if precision == "" {
		t.Errorf("precision is empty, want a non-empty value")
	}
}

// TestBuildIndex_PythonImportLandsInImportsTable is F.7/F.8's end-to-end
// wiring pin: a real buildIndex run over a Python file containing an import
// must populate the imports table with a row whose precision is non-empty
// (confirming the wiring reaches writeGraphDB's INSERT, not just the pure
// mapperImports function).
func TestBuildIndex_PythonImportLandsInImportsTable(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "a.py", "import foo.bar\n\ndef use():\n    pass\n")

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

	var importerFile, importedModule, precision string
	err = conn.QueryRow(`SELECT importer_file, imported_module, precision FROM imports
		WHERE imported_module = ?`, "foo.bar").Scan(&importerFile, &importedModule, &precision)
	if err != nil {
		t.Fatalf("read imports row: %v", err)
	}
	if importedModule != "foo.bar" {
		t.Errorf("imported_module = %q, want %q", importedModule, "foo.bar")
	}
	if precision == "" {
		t.Errorf("precision is empty, want a non-empty value")
	}
}

// TestTSImports_CoversEveryFormAndNeverOverCaptures pins both halves of
// tsImportsQuery's contract in one file: every module-naming form yields its
// specifier, and a plain single-string call does NOT. The second half is the
// load-bearing one — the last pattern matches any `f("...")` call and is
// narrowed to require() only by its #eq? predicate, so without this a
// dropped or unsupported predicate would silently turn every string-argument
// call in the repo into an import row.
func TestTSImports_CoversEveryFormAndNeverOverCaptures(t *testing.T) {
	dir := t.TempDir()
	f := mapMapperFile(t, dir, "a.ts", `import defaultExport from "./a";
import { named, other as alias } from "./b";
import * as ns from "./c";
import type { OnlyType } from "./d";
import "./side-effect";
const lazy = await import("./dynamic");
export { re } from "./e";
export * from "./f";
const cjs = require("./g");
notRequire("./NOT-AN-IMPORT");
console.log("./ALSO-NOT");
`, "a")

	rows, _, stats := mapperImports([]mapperFile{f})
	if stats.parseErr != 0 || stats.readErr != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	got := map[string]bool{}
	for _, r := range rows {
		got[r.importedModule] = true
	}
	for _, want := range []string{"./a", "./b", "./c", "./d", "./side-effect", "./dynamic", "./e", "./f", "./g"} {
		if !got[want] {
			t.Errorf("missing specifier %q; got %v", want, got)
		}
	}
	for _, never := range []string{"./NOT-AN-IMPORT", "./ALSO-NOT"} {
		if got[never] {
			t.Errorf("over-captured %q as an import: the require() #eq? predicate is not narrowing", never)
		}
	}
}

// ---------- G2.1 / G2.2: binding names ----------

// TestTSBindings_AliasedNamedImportRecordsLocalNameOnly is G2.1. For
// `import { Foo as Bar } from "./x"`, the name in scope in this file is Bar;
// Foo is NOT bound here at all. Recording Foo would let rung 2a fire on a
// receiver named Foo that actually refers to something else entirely, narrow
// the candidate set to the wrong file, and STOP the ladder — a miss, which
// the over-approximation contract does not permit. So the absence of Foo is
// the load-bearing assertion here, not an incidental detail.
func TestTSBindings_AliasedNamedImportRecordsLocalNameOnly(t *testing.T) {
	dir := t.TempDir()
	f := mapMapperFile(t, dir, "a.ts", "import { Foo as Bar, Plain } from \"./x\";\n", "a")

	_, bindings, stats := mapperImports([]mapperFile{f})
	if stats.parseErr != 0 || stats.readErr != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	got := bindings[f.rel]
	if got["Bar"] != "./x" {
		t.Errorf("bindings[%q][\"Bar\"] = %q, want %q", f.rel, got["Bar"], "./x")
	}
	if got["Plain"] != "./x" {
		t.Errorf("bindings[%q][\"Plain\"] = %q, want %q", f.rel, got["Plain"], "./x")
	}
	if _, ok := got["Foo"]; ok {
		t.Errorf("recorded %q as a binding, but only the alias %q is in scope: %v", "Foo", "Bar", got)
	}
}

// TestTSBindings_DefaultAndNamespaceImports is G2.2: the default-import form
// (`import Baz from "./y"`) and the namespace form (`import * as ns from
// "./c"`) each bind exactly one local name.
func TestTSBindings_DefaultAndNamespaceImports(t *testing.T) {
	dir := t.TempDir()
	f := mapMapperFile(t, dir, "a.ts", "import Baz from \"./y\";\nimport * as ns from \"./c\";\n", "a")

	_, bindings, _ := mapperImports([]mapperFile{f})
	got := bindings[f.rel]
	if got["Baz"] != "./y" {
		t.Errorf("bindings[\"Baz\"] = %q, want %q", got["Baz"], "./y")
	}
	if got["ns"] != "./c" {
		t.Errorf("bindings[\"ns\"] = %q, want %q", got["ns"], "./c")
	}
}

// TestBindings_SideEffectImportAndPythonBindNothing pins the two forms that
// must produce no binding at all: a side-effect import introduces no local
// name, and Python is not wired for bindings (rung 2a is JS-family only —
// Python still gets its import ROWS via gts.ExtractImports).
func TestBindings_SideEffectImportAndPythonBindNothing(t *testing.T) {
	dir := t.TempDir()
	ts := mapMapperFile(t, dir, "a.ts", "import \"./side-effect\";\n", "a")
	py := mapMapperFile(t, dir, "b.py", "import foo.bar\n", "b")

	rows, bindings, _ := mapperImports([]mapperFile{ts, py})
	if len(bindings[ts.rel]) != 0 {
		t.Errorf("side-effect import bound %v, want nothing", bindings[ts.rel])
	}
	if len(bindings[py.rel]) != 0 {
		t.Errorf("python bound %v, want nothing", bindings[py.rel])
	}
	if len(rows) != 2 {
		t.Errorf("len(rows) = %d, want 2 (both files still produce import ROWS)", len(rows))
	}
}
