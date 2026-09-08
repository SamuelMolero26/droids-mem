package graph

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	_ "modernc.org/sqlite"
)

// TestDetectDirective_Unit exercises parsed directive-prologue semantics.
func TestDetectDirective_Unit(t *testing.T) {
	lang := grammars.TsxLanguage()
	parsers := gts.NewParserPool(lang)
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"double client at top", "\"use client\";\nimport x from \"y\";\n", "client"},
		{"single client at top", "'use client';\nexport function Foo() {}\n", "client"},
		{"double server at top", "\"use server\";\nexport async function createPost() {}\n", "server"},
		{"single server at top", "'use server';\nexport async function createPost() {}\n", "server"},
		{"line comment before client", "// generated file\n'use client';\n", "client"},
		{"block comment before server", "/* generated file */\n'use server';\n", "server"},
		{"license banner before client", "/*!\n * Licensed source\n */\n'use client';\n", "client"},
		{"no directive", "import x from \"y\";\nexport function Foo() {}\n", ""},
		{"client not at top", "import x from \"y\";\n\"use client\";\n", ""},
		{"client after variable", "const x = 1;\n\"use client\";\n", ""},
		{"server inside function", "export function Foo() {\n\"use server\";\n}\n", ""},
		{"client side string", "\"use client-side\";\n", ""},
		{"client concatenated with suffix", "\"use client\" + suffix;\n", ""},
		{"client method call", "\"use client\".trim();\n", ""},
		{"client comment then expression", "\"use client\" /* comment */ + x;\n", ""},
		{"client followed by garbage", "\"use client\" garbage;\n", ""},
		{"client template literal", "`use client`;\n", ""},
		{"client escaped string", "\"use\\x20client\";\n", ""},
		{"client string in declaration", "const value = \"use client\";\n", ""},
		{"with BOM client", "\xEF\xBB\xBF\"use client\";\n", "client"},
		{"shebang then client", "#!/usr/bin/env node\n\"use client\";\n", "client"},
		{"shebang then server", "#!/usr/bin/env node\n'use server';\n", "server"},
		{"BOM plus shebang client", "\xEF\xBB\xBF#!/usr/bin/env node\n\"use client\";\n", "client"},
		{"BOM comments then client", "\xEF\xBB\xBF/* generated */\r\n\"use client\";\r\n", "client"},
		{"shebang comments then server", "#!/usr/bin/env node\n// generated\n\"use server\";\n", "server"},
		{"shebang CRLF comments then client", "#!/usr/bin/env node\r\n/* generated */\r\n\"use client\";\r\n", "client"},
		{"BOM shebang comments then server", "\xEF\xBB\xBF#!/usr/bin/env node\r\n/* generated */\r\n\"use server\";\r\n", "server"},
		{"with leading whitespace client", "  \n  \"use client\";\n", "client"},
		{"CRLF client", "\r\n\"use client\";\r\n", "client"},
		{"use strict then client", "\"use strict\";\n\"use client\";\n", "client"},
		{"use strict single then server", "'use strict';\n'use server';\n", "server"},
		{"arbitrary directive then client", "\"custom directive\";\n\"use client\";\n", "client"},
		{"comments between directives", "\"custom directive\";\n/* generated */\n\"use server\";\n", "server"},
		{"semicolonless client", "\"use client\"\nexport function Foo() {}\n", "client"},
		{"semicolonless directives", "\"custom directive\"\n\"use client\"\nexport function Foo() {}\n", "client"},
		{"semicolonless continuation", "\"use client\"\n.trim();\n", ""},
		{"parenthesized client", "(\"use client\");\n", ""},
		{"duplicate client directives", "\"use client\";\n'use client';\n", "client"},
		{"client and server conflict", "\"use client\";\n\"use server\";\n", ""},
		{"server and client conflict", "\"use server\";\n\"use client\";\n", ""},
		{"empty file", "", ""},
		{"only whitespace", "   \n\t  ", ""},
		{"long leading whitespace", strings.Repeat(" ", 400) + "\"use client\";\n", "client"},
		{"longer leading whitespace", strings.Repeat(" ", 499) + "\"use client\";\n", "client"},
		{"very long leading comment", "/*" + strings.Repeat("x", 4096) + "*/\n\"use client\";\n", "client"},
		{"directive text only in comment", " // \"use client\"\nimport x from \"y\";\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := []byte(tt.src)
			tree, err := parsers.Parse(src)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			defer tree.Release()
			got := detectDirective(src, tree, lang)
			if got != tt.want {
				t.Errorf("detectDirective(%q) = %q, want %q", tt.src[:min(len(tt.src), 60)], got, tt.want)
			}
		})
	}
}

// TestDirective_HintOnSymbol is the integration test for P3: a repo with a
// "use client" file and a "use server" file must surface those pragmas via
// SymbolResponse.Hint, while a regular file must not.
func TestDirective_HintOnSymbol(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "app/page.tsx", "\"use client\";\n\nimport { Button } from \"@/components/ui/button\";\n\nexport default function HomePage() {\n  return null;\n}\nexport function helperA(x: number): number { return x * 2; }\n")
	writeFile(t, repo, "app/actions.ts", "\"use server\";\n\nexport async function createPost(formData: FormData) {\n  return { title: \"hi\" };\n}\nexport async function deletePost(id: string) { return { deleted: id }; }\n")
	writeFile(t, repo, "lib/utils.ts", "export function cn(x: string): string { return x; }\n")

	m := managerFor(t)
	ctx := context.Background()

	resp, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "HomePage"})
	if err != nil {
		t.Fatalf("Symbol HomePage: %v", err)
	}
	if resp.Symbol == nil {
		t.Fatalf("Symbol HomePage: nil response %#v", resp)
	}
	if !strings.Contains(resp.Hint, "client") {
		t.Errorf("HomePage hint = %q, want it to contain \"client\" (use client directive)", resp.Hint)
	}
	if !strings.Contains(resp.Hint, clientDirectiveHint) {
		t.Errorf("HomePage hint = %q, want it to contain %q", resp.Hint, clientDirectiveHint)
	}

	resp, err = m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "createPost"})
	if err != nil {
		t.Fatalf("Symbol createPost: %v", err)
	}
	if resp.Symbol == nil {
		t.Fatalf("Symbol createPost: nil response")
	}
	if !strings.Contains(resp.Hint, "server") {
		t.Errorf("createPost hint = %q, want it to contain \"server\" (use server directive)", resp.Hint)
	}
	if !strings.Contains(resp.Hint, serverDirectiveHint) {
		t.Errorf("createPost hint = %q, want it to contain %q", resp.Hint, serverDirectiveHint)
	}

	resp, err = m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "cn"})
	if err != nil {
		t.Fatalf("Symbol cn: %v", err)
	}
	if strings.Contains(resp.Hint, "use client") || strings.Contains(resp.Hint, "use server") {
		t.Errorf("cn hint = %q, want no directive hint", resp.Hint)
	}
	if strings.Contains(resp.Hint, clientDirectiveHint) || strings.Contains(resp.Hint, serverDirectiveHint) {
		t.Errorf("cn hint unexpectedly carries directive hint: %q", resp.Hint)
	}
}

// TestDirective_PersistsToDB verifies file_directives table is written.
func TestDirective_PersistsToDB(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "a.tsx", "\"use client\";\nexport function Foo() {}\n")
	writeFile(t, repo, "b.ts", "\"use server\";\nexport function Bar() {}\n")
	writeFile(t, repo, "c.ts", "export function Baz() {}\n")

	dbPath := filepath.Join(t.TempDir(), "graph.db")
	st, err := stamp(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := buildIndex(context.Background(), repo, dbPath, st); err != nil {
		t.Fatalf("buildIndex: %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	rows, err := db.Query(`SELECT file, directive FROM file_directives ORDER BY file`)
	if err != nil {
		t.Fatalf("query file_directives: %v", err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var f, d string
		if err := rows.Scan(&f, &d); err != nil {
			t.Fatal(err)
		}
		got[f] = d
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if got["a.tsx"] != "client" {
		t.Errorf("a.tsx directive = %q, want client", got["a.tsx"])
	}
	if got["b.ts"] != "server" {
		t.Errorf("b.ts directive = %q, want server", got["b.ts"])
	}
	if _, ok := got["c.ts"]; ok {
		t.Errorf("c.ts should not be in file_directives, got %v", got)
	}
	if len(got) != 2 {
		t.Errorf("file_directives count = %d, want 2 (only directive files)", len(got))
	}
}

func TestDirective_NormalRebuildReplacesAndRemovesRow(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "app.ts", "'use client';\nexport function render() {}\n")

	m := managerFor(t)
	if _, err := m.Index(context.Background(), repo); err != nil {
		t.Fatalf("client index: %v", err)
	}
	if got, ok := graphDirective(t, m, repo, "app.ts"); !ok || got != "client" {
		t.Fatalf("client directive = %q, %v; want client, true", got, ok)
	}

	writeFile(t, repo, "app.ts", "'use server';\nexport function render() {}\n")
	if _, err := m.Index(context.Background(), repo); err != nil {
		t.Fatalf("server index: %v", err)
	}
	if got, ok := graphDirective(t, m, repo, "app.ts"); !ok || got != "server" {
		t.Fatalf("server directive = %q, %v; want server, true", got, ok)
	}

	writeFile(t, repo, "app.ts", "export function render() {}\n")
	if _, err := m.Index(context.Background(), repo); err != nil {
		t.Fatalf("no-directive index: %v", err)
	}
	if got, ok := graphDirective(t, m, repo, "app.ts"); ok {
		t.Fatalf("removed directive persisted as %q", got)
	}
	resp, err := m.Symbol(context.Background(), SymbolRequest{Repo: repo, Symbol: "render"})
	if err != nil {
		t.Fatalf("Symbol render: %v", err)
	}
	if strings.Contains(resp.Hint, clientDirectiveHint) || strings.Contains(resp.Hint, serverDirectiveHint) {
		t.Errorf("removed directive remained in hint %q", resp.Hint)
	}
}

func TestManager_FileDirectivesSchemaAdditionInvalidatesOldGraph(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "app.ts", "'use client';\nexport function render() {}\n")

	m := managerFor(t)
	canon, err := canonicalRepo(repo)
	if err != nil {
		t.Fatal(err)
	}
	dbPath := m.dbPath(canon)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o750); err != nil {
		t.Fatal(err)
	}

	const directiveDDL = `CREATE TABLE file_directives (
  file      TEXT PRIMARY KEY,
  directive TEXT NOT NULL
) WITHOUT ROWID;
`
	oldSchema := strings.Replace(schema, directiveDDL, "", 1)
	if oldSchema == schema {
		t.Fatal("test setup: file_directives DDL not found in schema")
	}
	st, err := stamp(repo)
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(st, ":")
	if !ok {
		t.Fatalf("stamp = %q, want generation separator", st)
	}
	oldStamp := stampGen(oldSchema, indexedExtensions(), indexerGen) + ":" + rest
	if strings.HasPrefix(st, stampGen(oldSchema, indexedExtensions(), indexerGen)+":") {
		t.Fatalf("schema addition did not move current generation: current stamp %q", st)
	}

	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(oldSchema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"stamp": oldStamp, "indexed_at": "2026-01-01T00:00:00Z", "carried_units": "",
	} {
		if _, err := db.Exec(`INSERT INTO meta (key, value) VALUES (?,?)`, k, v); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	wait, err := m.WaitBuild(context.Background(), repo, 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !wait.Completed || !wait.Rebuilt {
		t.Fatalf("old schema graph was not rebuilt: %+v", wait)
	}
	if got, ok := graphDirective(t, m, repo, "app.ts"); !ok || got != "client" {
		t.Fatalf("rebuilt directive = %q, %v; want client, true", got, ok)
	}
}

func graphDirective(t *testing.T, m *Manager, repo, file string) (string, bool) {
	t.Helper()
	db := openGraphTestDB(t, m, repo)
	defer db.Close()
	var directive string
	err := db.QueryRow(`SELECT directive FROM file_directives WHERE file = ?`, file).Scan(&directive)
	if err == sql.ErrNoRows {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return directive, true
}

func graphMeta(t *testing.T, m *Manager, repo, key string) string {
	t.Helper()
	db := openGraphTestDB(t, m, repo)
	defer db.Close()
	var value string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func openGraphTestDB(t *testing.T, m *Manager, repo string) *sql.DB {
	t.Helper()
	canon, err := canonicalRepo(repo)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+m.dbPath(canon)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	return db
}
