package graph

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	gts "github.com/odvcencio/gotreesitter"
)

const swiftOutlineFixture = `import Foundation

protocol Loader {
    func load(path: String) -> String
}

public struct Config {
    let name: String
    init(name: String) { self.name = name }
    func normalize() -> String { return helper(name) }
    private func secret() {}
}

extension Config {
    func render() -> String { return normalize() }
}

private extension Config {
    func hidden() {}
}

fileprivate func helper(_ s: String) -> String { s }

func overloaded(_ a: Int) {}
func overloaded(_ a: String) {}
`

// TestMapperSymbols_SwiftOutline pins the custom Swift tags query: types,
// protocols and their requirements, methods, initializers and properties all
// outline, nested under their container — including members declared in an
// extension, which must land under the extended type, not at the top level.
func TestMapperSymbols_SwiftOutline(t *testing.T) {
	f := mapMapperFile(t, t.TempDir(), "Config.swift", swiftOutlineFixture, "Sources/App/Config")
	rows, stats := scanSymbols([]mapperFile{f})
	if stats.parseErr != 0 || stats.outlineDecline != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}

	tests := []struct {
		name  string
		qname string
		kind  string
	}{
		{"Loader", "Sources/App/Config:Loader", "interface"},
		{"load", "Sources/App/Config:Loader.load", "method"},
		{"Config", "Sources/App/Config:Config", "class"},
		{"name", "Sources/App/Config:Config.name", "var"},
		{"init", "Sources/App/Config:Config.init", "constructor"},
		{"normalize", "Sources/App/Config:Config.normalize", "func"},
		{"render", "Sources/App/Config:Config.render", "func"},
		{"hidden", "Sources/App/Config:Config.hidden", "func"},
		{"helper", "Sources/App/Config:helper", "func"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := findSymRow(t, rows, tt.name)
			if row.qname != tt.qname {
				t.Errorf("qname = %q, want %q", row.qname, tt.qname)
			}
			if row.kind != tt.kind {
				t.Errorf("kind = %q, want %q", row.kind, tt.kind)
			}
		})
	}
}

// TestMapperSymbols_SwiftLocalsNotSymbols pins that a let/var inside a function
// or initializer body is a local, not a symbol: only top-level and member
// properties outline. Measured on a real app, locals were ~37% of all symbols.
func TestMapperSymbols_SwiftLocalsNotSymbols(t *testing.T) {
	src := `let shared = 1
struct S {
    var member = 0
    init() { let tmp = 2; member = tmp }
    func run() -> Int { var acc = 0; acc += member; return acc }
}
extension S {
    var computed: Int { let inner = 3; return inner }
}
`
	f := mapMapperFile(t, t.TempDir(), "S.swift", src, "Sources/App/S")
	rows, _ := scanSymbols([]mapperFile{f})
	var got []string
	for _, r := range rows {
		if r.row.kind == "var" && !slices.Contains(got, r.row.qname) {
			got = append(got, r.row.qname)
		}
	}
	slices.Sort(got)
	want := []string{"Sources/App/S:S.computed", "Sources/App/S:S.member", "Sources/App/S:shared"}
	if !slices.Equal(got, want) {
		t.Fatalf("var symbols = %v, want %v", got, want)
	}
}

// TestMapperSymbols_SwiftExported pins the exported rule: Swift's default
// access is internal, so everything is exported except private/fileprivate
// declarations and members of a private extension.
func TestMapperSymbols_SwiftExported(t *testing.T) {
	f := mapMapperFile(t, t.TempDir(), "Config.swift", swiftOutlineFixture, "Config")
	rows, _ := scanSymbols([]mapperFile{f})

	tests := []struct {
		name string
		want bool
	}{
		{"Config", true},
		{"normalize", true},
		{"render", true},
		{"secret", false},
		{"helper", false},
		{"hidden", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := findSymRow(t, rows, tt.name).exported; got != tt.want {
				t.Errorf("exported = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestMapperSymbols_SwiftSameQNameMerged pins that overloads, and a type plus
// its same-file extension, share one row: two rows with one qname dead-end
// graph_symbol.
func TestMapperSymbols_SwiftSameQNameMerged(t *testing.T) {
	f := mapMapperFile(t, t.TempDir(), "Config.swift", swiftOutlineFixture, "Config")
	rows, _ := scanSymbols([]mapperFile{f})

	byQName := map[string]map[*symRow]bool{}
	for _, r := range rows {
		if byQName[r.row.qname] == nil {
			byQName[r.row.qname] = map[*symRow]bool{}
		}
		byQName[r.row.qname][r.row] = true
	}
	for qname, distinct := range byQName {
		if len(distinct) != 1 {
			t.Errorf("qname %q has %d distinct rows, want 1", qname, len(distinct))
		}
	}
	if src := findSymRow(t, rows, "overloaded").source; !strings.Contains(src, "Int") || !strings.Contains(src, "String") {
		t.Errorf("merged overload source = %q, want both overloads", src)
	}
}

// TestScanMapperFile_ParseTimeoutCountedAsParseError pins the per-file parse
// budget: a parse that stops on its timeout yields a partial tree, which must
// be skipped and counted like any other parse failure, never outlined.
func TestScanMapperFile_ParseTimeoutCountedAsParseError(t *testing.T) {
	var b strings.Builder
	for i := range 3000 {
		b.WriteString("func f")
		b.WriteString(strings.Repeat("x", i%7+1))
		b.WriteString("() { print(\"a\") }\n")
	}
	f := mapMapperFile(t, t.TempDir(), "Big.swift", b.String(), "Big")
	eng := mapperEngines{}.get(f.entry)
	eng.parsers = gts.NewParserPool(eng.lang, gts.WithParserPoolTimeoutMicros(1))

	res := mapperScanResult{bindings: mapperImportBindings{}, hasError: map[string]bool{}}
	if scanMapperFile(eng, f, []byte(b.String()), &res) {
		t.Fatal("scanMapperFile = true, want false for a timed-out parse")
	}
	if res.stats.parseErr != 1 {
		t.Errorf("parseErr = %d, want 1", res.stats.parseErr)
	}
	if len(res.syms) != 0 {
		t.Errorf("got %d symbols from a timed-out parse, want 0", len(res.syms))
	}
}

// TestCallsFromMapperTree_SwiftCallRefs pins the Swift call query that stands
// in for FactCalls (which returns nothing for Swift): bare, self-qualified and
// navigation calls all yield a CallRef whose Receiver is the text before the
// called name.
func TestCallsFromMapperTree_SwiftCallRefs(t *testing.T) {
	src := `func run() {
    helper()
    self.load(path: "x")
    store.items.map { $0 }
    cache?.flush()
    builder
        .finish()
}
`
	f := mapMapperFile(t, t.TempDir(), "Run.swift", src, "Run")
	fileCalls, _ := scanCalls([]mapperFile{f})
	if len(fileCalls) != 1 {
		t.Fatalf("got %d files with calls, want 1", len(fileCalls))
	}
	var got []string
	for _, r := range fileCalls[0].refs {
		got = append(got, r.Receiver+"|"+r.Name)
		if name := src[r.NameStartByte:r.NameEndByte]; name != r.Name {
			t.Errorf("name bytes = %q, want %q", name, r.Name)
		}
		if r.StartByte >= r.EndByte {
			t.Errorf("call %q has empty byte range", r.Name)
		}
	}
	slices.Sort(got)
	want := []string{"self|load", "store.items|map", "cache|flush", "builder|finish", "|helper"}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("refs = %v, want %v", got, want)
	}
}

// TestBuildIndex_SwiftCallEdges runs the real build over a two-file Swift
// module: an explicit self call and an implicit-self call both resolve to the
// enclosing type's method through the existing ladder, and a free-function
// call resolves across files.
func TestBuildIndex_SwiftCallEdges(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "Sources", "App"), "Config.swift", `struct Config {
    func normalize() -> String { return helper() }
    func load() {}
    func run() {
        self.load()
        _ = normalize()
    }
}
`)
	writeFile(t, filepath.Join(repo, "Sources", "App"), "Util.swift", "func helper() -> String { return \"x\" }\n")

	dbPath := filepath.Join(t.TempDir(), "graph.db")
	st, err := stamp(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := buildIndex(context.Background(), repo, dbPath, snapshot{stamp: st}); err != nil {
		t.Fatalf("buildIndex: %v", err)
	}
	conn, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	rows, err := conn.Query(`SELECT s1.qname, s2.qname FROM edges e
		JOIN symbols s1 ON s1.id = e.caller
		JOIN symbols s2 ON s2.id = e.callee`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var caller, callee string
		if err := rows.Scan(&caller, &callee); err != nil {
			t.Fatal(err)
		}
		got = append(got, caller+" -> "+callee)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	want := []string{
		"Sources/App/Config:Config.normalize -> Sources/App/Util:helper",
		"Sources/App/Config:Config.run -> Sources/App/Config:Config.load",
		"Sources/App/Config:Config.run -> Sources/App/Config:Config.normalize",
	}
	if !slices.Equal(got, want) {
		t.Errorf("edges = %v, want %v", got, want)
	}
}
