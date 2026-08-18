package graph

import (
	"path/filepath"
	"strings"
	"testing"

	gts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// mapMapperFile writes content under dir/name and returns the mapperFile the
// discovery stage would have produced for it, without going through the
// walk — conversion (mapper_symbols.go) is tested independently of
// discovery (mapper.go), matching their file-boundary split (design.md
// decision 2).
func mapMapperFile(t *testing.T, dir, name, content, modPath string) mapperFile {
	t.Helper()
	writeFile(t, dir, name, content)
	entry := grammars.DetectLanguage(name)
	if entry == nil {
		t.Fatalf("DetectLanguage(%q) = nil", name)
	}
	return mapperFile{
		abs:        filepath.Join(dir, name),
		rel:        name,
		entry:      entry,
		modulePath: modPath,
	}
}

func findSymRow(t *testing.T, rows []*symRow, name string) *symRow {
	t.Helper()
	for _, r := range rows {
		if r.name == name {
			return r
		}
	}
	t.Fatalf("no symRow named %q among %d rows", name, len(rows))
	return nil
}

func TestMapperSymbols_LineFromNameRange(t *testing.T) {
	dir := t.TempDir()
	f := mapMapperFile(t, dir, "a.ts", "// header\n// more header\nexport function Foo() {}\n", "a")

	rows, stats := mapperSymbols([]mapperFile{f})
	if stats.parseErr != 0 || stats.outlineDecline != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	row := findSymRow(t, rows, "Foo")
	if row.line != 3 {
		t.Errorf("line = %d, want 3 (NameRange.StartPoint.Row(2) + 1)", row.line)
	}
}

func TestMapperSymbols_SignatureAndSourceTruncation(t *testing.T) {
	dir := t.TempDir()
	longParams := strings.Repeat("a", 400)
	var body strings.Builder
	for body.Len() < maxSourceBytes+500 {
		body.WriteString("  // " + strings.Repeat("b", 200) + "\n")
	}
	src := "export function LongName(" + longParams + ") {\n" + body.String() + "}\n"
	f := mapMapperFile(t, dir, "a.ts", src, "a")

	rows, stats := mapperSymbols([]mapperFile{f})
	if stats.parseErr != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	row := findSymRow(t, rows, "LongName")

	if !strings.HasSuffix(row.signature, "…[truncated]") {
		t.Errorf("signature not truncated (len=%d): %q", len(row.signature), row.signature[:min(60, len(row.signature))])
	}
	if len(row.signature) > maxSigBytes+len("…[truncated]") {
		t.Errorf("signature too long: %d bytes, want <= %d", len(row.signature), maxSigBytes+len("…[truncated]"))
	}
	if !strings.HasSuffix(row.source, "…[truncated]") {
		t.Errorf("source not truncated (len=%d)", len(row.source))
	}
	if len(row.source) > maxSourceBytes+len("…[truncated]") {
		t.Errorf("source too long: %d bytes, want <= %d", len(row.source), maxSourceBytes+len("…[truncated]"))
	}
}

func TestMapperSymbols_DocAlwaysEmpty(t *testing.T) {
	dir := t.TempDir()
	ts := mapMapperFile(t, dir, "a.ts", "// this is a doc comment\nexport function Foo() {}\n", "a")
	py := mapMapperFile(t, dir, "b.py", "def foo():\n    \"\"\"docstring\"\"\"\n    pass\n", "b")

	rows, stats := mapperSymbols([]mapperFile{ts, py})
	if stats.parseErr != 0 || stats.outlineDecline != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if len(rows) == 0 {
		t.Fatal("no symbols produced")
	}
	for _, r := range rows {
		if r.doc != "" {
			t.Errorf("%s.doc = %q, want empty (doc is always \"\" in this slice)", r.name, r.doc)
		}
	}
}

func TestNormalizeOutlineKind(t *testing.T) {
	tests := []struct{ in, want string }{
		{"function", "func"},
		{"variable", "var"},
		{"constant", "const"},
		{"method", "method"},
		{"interface", "interface"},
		{"type", "type"},
		{"class", "class"},
		{"constructor", "constructor"},
		{"module", "module"},
	}
	for _, tt := range tests {
		if got := normalizeOutlineKind(tt.in); got != tt.want {
			t.Errorf("normalizeOutlineKind(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestMapperSymbols_ExportedTSJS(t *testing.T) {
	dir := t.TempDir()
	f := mapMapperFile(t, dir, "a.ts", "export function foo() {}\nfunction bar() {}\nexport function _foo() {}\n", "a")

	rows, _ := mapperSymbols([]mapperFile{f})
	want := map[string]bool{"foo": true, "bar": false, "_foo": true}
	for name, exp := range want {
		row := findSymRow(t, rows, name)
		if row.exported != exp {
			t.Errorf("%s.exported = %v, want %v", name, row.exported, exp)
		}
	}
}

func TestMapperSymbols_ExportedNestedInheritsContainer(t *testing.T) {
	dir := t.TempDir()
	f := mapMapperFile(t, dir, "a.ts", "export class Outer {\n  method() {}\n}\n", "a")

	rows, _ := mapperSymbols([]mapperFile{f})
	outer := findSymRow(t, rows, "Outer")
	method := findSymRow(t, rows, "method")
	if !outer.exported {
		t.Error("Outer.exported = false, want true")
	}
	if !method.exported {
		t.Error("method.exported = false, want true — nested symbols inherit their container's flag")
	}
}

// TestMapperSymbols_ExportedBlockScopedFunctionNotInherited pins design.md
// decision 6's climb-stop: a top-level symbol's ancestor climb must stop at
// the first scope node, so a function declared inside a plain block (not an
// export_statement) is never marked exported merely because some unrelated
// declaration elsewhere in the file is.
func TestMapperSymbols_ExportedBlockScopedFunctionNotInherited(t *testing.T) {
	dir := t.TempDir()
	f := mapMapperFile(t, dir, "a.ts", "export function foo() {}\nif (true) {\n  function bar() {}\n}\n", "a")

	rows, _ := mapperSymbols([]mapperFile{f})
	bar := findSymRow(t, rows, "bar")
	if bar.exported {
		t.Error("bar.exported = true, want false — block-scoped, must not inherit an unrelated top-level export")
	}
}

func TestMapperSymbols_ExportedPython(t *testing.T) {
	dir := t.TempDir()
	f := mapMapperFile(t, dir, "a.py", "def foo(): pass\ndef _bar(): pass\n", "a")

	rows, _ := mapperSymbols([]mapperFile{f})
	foo := findSymRow(t, rows, "foo")
	bar := findSymRow(t, rows, "_bar")
	if !foo.exported {
		t.Error("foo.exported = false, want true")
	}
	if bar.exported {
		t.Error("_bar.exported = true, want false")
	}
}

func TestMapperSymbols_QnameContainerChain(t *testing.T) {
	dir := t.TempDir()
	f := mapMapperFile(t, dir, "a.ts", "export class Outer {\n  method() {}\n}\n", "src/util")

	rows, _ := mapperSymbols([]mapperFile{f})
	outer := findSymRow(t, rows, "Outer")
	method := findSymRow(t, rows, "method")
	if outer.qname != "src/util:Outer" {
		t.Errorf("Outer.qname = %q, want src/util:Outer", outer.qname)
	}
	if method.qname != "src/util:Outer.method" {
		t.Errorf("method.qname = %q, want src/util:Outer.method", method.qname)
	}
	for _, r := range rows {
		if !strings.Contains(r.qname, ":") {
			t.Errorf("%s.qname = %q missing the colon separator", r.name, r.qname)
		}
	}

	// The colon convention exists to never collide with the Go tier's
	// dot-separated "pkg.Name" qname construction (index.go's add()).
	goQname := "pkg" + "." + "Name"
	if strings.Contains(goQname, ":") {
		t.Error("Go-tier qname construction unexpectedly contains ':' — collision risk with mapper qnames")
	}
}

// TestMapperSymbols_ReadErrorCountedSkipAndContinue pins skip-and-continue
// for os.ReadFile failures: one bad file must not stop the rest of the run.
func TestMapperSymbols_ReadErrorCountedSkipAndContinue(t *testing.T) {
	dir := t.TempDir()
	good := mapMapperFile(t, dir, "good.py", "def foo(): pass\n", "good")
	bad := mapperFile{abs: filepath.Join(dir, "missing.py"), rel: "missing.py", entry: good.entry, modulePath: "missing"}

	rows, stats := mapperSymbols([]mapperFile{bad, good})
	if stats.readErr != 1 {
		t.Errorf("readErr = %d, want 1", stats.readErr)
	}
	if len(rows) != 1 || rows[0].name != "foo" {
		t.Errorf("walk did not continue past the read error: rows=%+v", rows)
	}
}

// TestMapperSymbols_EngineLoadFailureCountedSkipAndContinue simulates a
// language whose grammar failed to load lazily (Language() returns nil) —
// no parse is possible at all, counted as a parse failure.
func TestMapperSymbols_EngineLoadFailureCountedSkipAndContinue(t *testing.T) {
	dir := t.TempDir()
	good := mapMapperFile(t, dir, "good.py", "def foo(): pass\n", "good")
	broken := mapMapperFile(t, dir, "broken.py", "def bar(): pass\n", "broken")
	broken.entry = &grammars.LangEntry{Name: "broken-lang-probe", Language: func() *gts.Language { return nil }}

	rows, stats := mapperSymbols([]mapperFile{broken, good})
	if stats.parseErr != 1 {
		t.Errorf("parseErr = %d, want 1", stats.parseErr)
	}
	if len(rows) != 1 || rows[0].name != "foo" {
		t.Errorf("walk did not continue past the engine load failure: rows=%+v", rows)
	}
}

// TestMapperSymbols_OutlineDeclineCountedSkipAndContinue reproduces an
// outliner with no tags data for an otherwise-parseable file: a real
// Language (so Parse succeeds) paired with an entry whose Name and
// TagsQuery are both empty, so grammars.ResolveTagsQuery resolves to "".
// This does not depend on any real grammar actually lacking tags data.
func TestMapperSymbols_OutlineDeclineCountedSkipAndContinue(t *testing.T) {
	dir := t.TempDir()
	realPy := grammars.DetectLanguage("x.py")
	if realPy == nil {
		t.Fatal("DetectLanguage(x.py) = nil")
	}
	good := mapMapperFile(t, dir, "good.py", "def foo(): pass\n", "good")
	declined := mapMapperFile(t, dir, "declined.py", "def bar(): pass\n", "declined")
	declined.entry = &grammars.LangEntry{Name: "", Language: realPy.Language}

	rows, stats := mapperSymbols([]mapperFile{declined, good})
	if stats.outlineDecline != 1 {
		t.Errorf("outlineDecline = %d, want 1", stats.outlineDecline)
	}
	if len(rows) != 1 || rows[0].name != "foo" {
		t.Errorf("walk did not continue past the outline decline: rows=%+v", rows)
	}
}
