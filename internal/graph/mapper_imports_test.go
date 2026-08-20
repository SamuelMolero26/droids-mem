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

	rows, stats := mapperImports([]mapperFile{f})
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

// TestPythonImports_TSFileProducesNoRows pins the scope boundary explicitly:
// this slice only wires Python (design docs: gts.ExtractImports' non-Python
// language support here is Go/Java/Starlark, none of which are mapper
// languages; TS/JS is PR-G1/G2), so a .ts file must never produce an imports
// row via this path.
func TestPythonImports_TSFileProducesNoRows(t *testing.T) {
	dir := t.TempDir()
	f := mapMapperFile(t, dir, "a.ts", "import { Bar } from \"./x\";\n", "a")

	rows, _ := mapperImports([]mapperFile{f})
	if len(rows) != 0 {
		t.Errorf("len(rows) = %d, want 0 for a non-Python file: %+v", len(rows), rows)
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
