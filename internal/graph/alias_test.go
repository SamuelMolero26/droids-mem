package graph

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestResolveSpecifier_AliasFallback covers the naive "@/→./" mapping that
// fires when no tsconfig/jsconfig exists. This is the 95% create-next-app
// case and the core of P2: before the fix, both returned "" and rung 2a never
// fired.
func TestResolveSpecifier_AliasFallback(t *testing.T) {
	known := map[string]bool{
		"components/ui/button.tsx": true,
		"lib/utils.ts":             true,
	}
	cases := []struct {
		spec string
		want string
	}{
		{"@/components/ui/button", "components/ui/button.tsx"},
		{"@/lib/utils", "lib/utils.ts"},
		{"next/link", ""},
		{"next/font/google", ""},
		{"react", ""},
		{"@/missing", ""},
		{"./relative", ""}, // known doesn't contain relative target
	}
	for _, tc := range cases {
		t.Run(tc.spec, func(t *testing.T) {
			got := resolveSpecifier("app/page.tsx", tc.spec, known, nil)
			if got != tc.want {
				t.Errorf("resolveSpecifier(app/page.tsx, %q) = %q, want %q", tc.spec, got, tc.want)
			}
		})
	}
}

// TestResolveSpecifier_AliasCustomPaths ensures a tsconfig with a custom
// prefix "@components/*": ["src/components/*"] is honoured via longest-prefix
// match and that its baseUrl is respected.
func TestResolveSpecifier_AliasCustomPaths(t *testing.T) {
	// Repo with baseUrl "." and a custom prefix.
	cfg1 := &aliasConfig{
		baseUrl: ".",
		paths: map[string][]string{
			"@components/*": {"src/components/*"},
			"@/*":           {"./*"},
		},
	}
	known := map[string]bool{
		"src/components/ui/button.tsx": true,
		"src/lib/utils.ts":             true,
		"components/ui/button.tsx":     true,
	}
	// "@components/..." should win over "@/*" due to longer prefix.
	if got := resolveSpecifier("app/page.tsx", "@components/ui/button", known, cfg1); got != "src/components/ui/button.tsx" {
		t.Errorf("custom prefix resolve = %q, want %q", got, "src/components/ui/button.tsx")
	}
	// "@/*" still works for other files not matching the longer prefix.
	if got := resolveSpecifier("app/page.tsx", "@/lib/utils", known, &aliasConfig{baseUrl: ".", paths: map[string][]string{"@/*": {"./*"}}}); got != "" {
		// known has src/lib/utils.ts, not lib/utils.ts, so "@/lib/utils" via "./*" should not resolve to src/lib
		// Actually with "./*" and known lacking lib/utils.ts, it should be "".
		// This is expected: the alias maps to lib/utils.ts which is not in known.
		if got != "" {
			t.Errorf("expected no match for @/lib/utils without src prefix, got %q", got)
		}
	}
	// With correct known, "@/*" resolves.
	known2 := map[string]bool{"lib/utils.ts": true}
	if got := resolveSpecifier("app/page.tsx", "@/lib/utils", known2, &aliasConfig{baseUrl: ".", paths: map[string][]string{"@/*": {"./*"}}}); got != "lib/utils.ts" {
		t.Errorf("@/* resolve = %q, want %q", got, "lib/utils.ts")
	}
	// Bare stays unresolved even with alias.
	if got := resolveSpecifier("app/page.tsx", "next/link", known, cfg1); got != "" {
		t.Errorf("bare next/link should stay \"\", got %q", got)
	}
}

// TestResolveSpecifier_AliasBaseUrlHandling pins baseUrl semantics: when
// baseUrl is "src" and paths "@/*": ["*"], spec "@/utils" should resolve to
// "src/utils" (baseUrl joined with target).
func TestResolveSpecifier_AliasBaseUrlHandling(t *testing.T) {
	cfg := &aliasConfig{
		baseUrl: "src",
		paths: map[string][]string{
			"@/*": {"*"},
		},
	}
	known := map[string]bool{
		"src/utils.ts": true,
	}
	if got := resolveSpecifier("app/page.tsx", "@/utils", known, cfg); got != "src/utils.ts" {
		t.Errorf("baseUrl src resolve = %q, want %q", got, "src/utils.ts")
	}
	// baseUrl "src" with target "./components/*" and prefix "@components/*"
	cfg2 := &aliasConfig{
		baseUrl: "src",
		paths: map[string][]string{
			"@components/*": {"components/*"},
		},
	}
	known2 := map[string]bool{"src/components/button.tsx": true}
	if got := resolveSpecifier("app/page.tsx", "@components/button", known2, cfg2); got != "src/components/button.tsx" {
		t.Errorf("baseUrl+components resolve = %q, want %q", got, "src/components/button.tsx")
	}
}

// aliasRepo makes a temp repo that looks like its own checkout, so the alias
// walk-up stops there instead of picking up a stray tsconfig.json from an
// ancestor of the temp dir.
func aliasRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	writeFile(t, repo, ".git/HEAD", "ref: refs/heads/main\n")
	return repo
}

// aliasResolve runs the SHIPPED resolver — the exact call buildIndex makes —
// against a known-file set, so these cases pin the code that actually
// produces edges rather than a parallel helper.
func aliasResolve(cfg *aliasConfig, spec string, known ...string) string {
	set := make(map[string]bool, len(known))
	for _, k := range known {
		set[k] = true
	}
	return resolveSpecifier("app/page.tsx", spec, set, cfg)
}

// TestAliasConfig_LoadAndPaths tests loadAliasConfig's JSON parsing, baseUrl
// default, longest-prefix, wildcard replacement, and the naive fallback when
// no config exists. It also pins that a config file with no paths does NOT
// fall back to "@/→./".
func TestAliasConfig_LoadAndPaths(t *testing.T) {
	// 1. Naive fallback when no config at all.
	repoNoConfig := aliasRepo(t)
	if cfg := loadAliasConfig(repoNoConfig); cfg != nil {
		t.Fatalf("loadAliasConfig with no config = %#v, want nil", cfg)
	}
	if got := aliasResolve(nil, "@/lib/utils", "lib/utils.ts"); got != "lib/utils.ts" {
		t.Errorf("no config @/lib/utils = %q, want %q", got, "lib/utils.ts")
	}
	if got := aliasResolve(nil, "next/link", "lib/utils.ts"); got != "" {
		t.Errorf("no config next/link = %q, want empty", got)
	}
	// Only "@/" falls back — a custom prefix has no meaning without a config.
	if got := aliasResolve(nil, "@components/button", "components/button.ts"); got != "" {
		t.Errorf("no config @components/button = %q, want empty (only @/ fallback)", got)
	}

	// 2. Config with baseUrl "." and "@/*": ["./*"] → "@/lib/utils" => "lib/utils"
	repo := aliasRepo(t)
	writeFile(t, repo, "tsconfig.json", `{"compilerOptions":{"baseUrl":".","paths":{"@/*":["./*"]}}}`)
	cfg := loadAliasConfig(repo)
	if cfg == nil {
		t.Fatal("loadAliasConfig returned nil, want non-nil")
	}
	if cfg.baseUrl != "." {
		t.Errorf("baseUrl = %q, want %q", cfg.baseUrl, ".")
	}
	if got := aliasResolve(cfg, "@/lib/utils", "lib/utils.ts"); got != "lib/utils.ts" {
		t.Errorf("config @/lib/utils = %q, want %q", got, "lib/utils.ts")
	}
	if got := aliasResolve(cfg, "next/link", "lib/utils.ts"); got != "" {
		t.Errorf("config next/link = %q, want empty", got)
	}

	// 3. Custom prefix "@components/*": ["src/components/*"] with longest-prefix.
	repo2 := aliasRepo(t)
	writeFile(t, repo2, "tsconfig.json", `{"compilerOptions":{"baseUrl":".","paths":{"@/*":["./*"],"@components/*":["src/components/*"]}}}`)
	cfg2 := loadAliasConfig(repo2)
	if cfg2 == nil {
		t.Fatal("loadAliasConfig2 nil")
	}
	// Longest prefix wins: "@components/..." must not be captured by "@/*",
	// which would have produced "components/ui/button.tsx".
	if got := aliasResolve(cfg2, "@components/ui/button", "src/components/ui/button.tsx", "components/ui/button.tsx"); got != "src/components/ui/button.tsx" {
		t.Errorf("custom prefix = %q, want %q", got, "src/components/ui/button.tsx")
	}
	if got := aliasResolve(cfg2, "@/lib/utils", "lib/utils.ts"); got != "lib/utils.ts" {
		t.Errorf("fallback @/* = %q, want %q", got, "lib/utils.ts")
	}

	// 4. baseUrl "src" with "@/*": ["*"] → "@/utils" => "src/utils"
	repo3 := aliasRepo(t)
	writeFile(t, repo3, "tsconfig.json", `{"compilerOptions":{"baseUrl":"src","paths":{"@/*":["*"]}}}`)
	cfg3 := loadAliasConfig(repo3)
	if got := aliasResolve(cfg3, "@/utils", "src/utils.ts", "utils.ts"); got != "src/utils.ts" {
		t.Errorf("baseUrl src = %q, want %q", got, "src/utils.ts")
	}

	// 5. Config with no paths: must NOT fall back for "@/" — a repo that ships
	// a config without an "@/" entry genuinely has no "@/" alias, and inventing
	// one would produce edges TypeScript itself rejects.
	repo4 := aliasRepo(t)
	writeFile(t, repo4, "tsconfig.json", `{"compilerOptions":{"baseUrl":"."}}`)
	cfg4 := loadAliasConfig(repo4)
	if cfg4 == nil {
		t.Fatal("loadAliasConfig4 nil, want non-nil (file exists)")
	}
	if got := aliasResolve(cfg4, "@/lib/utils", "lib/utils.ts"); got != "" {
		t.Errorf("config with no paths = %q, want empty (no naive fallback when config exists)", got)
	}

	// 6. Extends: child inherits base baseUrl and paths, adds its own.
	repo5 := aliasRepo(t)
	writeFile(t, repo5, "tsconfig.base.json", `{"compilerOptions":{"baseUrl":"src","paths":{"@/*":["*"]}}}`)
	writeFile(t, repo5, "tsconfig.json", `{"extends":"./tsconfig.base.json","compilerOptions":{"paths":{"@utils/*":["utils/*"]}}}`)
	cfg5 := loadAliasConfig(repo5)
	if cfg5 == nil {
		t.Fatal("loadAliasConfig5 nil")
	}
	if cfg5.baseUrl != "src" {
		t.Errorf("extends baseUrl = %q, want %q", cfg5.baseUrl, "src")
	}
	if got := aliasResolve(cfg5, "@/utils", "src/utils.ts"); got != "src/utils.ts" {
		t.Errorf("extends inherits @/* = %q, want %q", got, "src/utils.ts")
	}
	if got := aliasResolve(cfg5, "@utils/foo", "src/utils/foo.ts"); got != "src/utils/foo.ts" {
		t.Errorf("extends child @utils/* = %q, want %q", got, "src/utils/foo.ts")
	}

	// 7. jsconfig.json fallback when tsconfig missing.
	repo6 := aliasRepo(t)
	writeFile(t, repo6, "jsconfig.json", `{"compilerOptions":{"baseUrl":".","paths":{"@/*":["./*"]}}}`)
	cfg6 := loadAliasConfig(repo6)
	if cfg6 == nil {
		t.Fatal("loadAliasConfig jsconfig nil")
	}
	if got := aliasResolve(cfg6, "@/a", "a.js"); got != "a.js" {
		t.Errorf("jsconfig = %q, want %q", got, "a.js")
	}

	// 8. JSON with comments and trailing commas (strip helpers).
	repo7 := aliasRepo(t)
	writeFile(t, repo7, "tsconfig.json", `{
		// comment line
		"compilerOptions": {
			"baseUrl": ".", // inline comment
			"paths": {
				"@/*": ["./*"], /* block comment */
			},
		},
	}`)
	cfg7 := loadAliasConfig(repo7)
	if cfg7 == nil {
		t.Fatal("loadAliasConfig comments nil")
	}
	if got := aliasResolve(cfg7, "@/x", "x.ts"); got != "x.ts" {
		t.Errorf("comments = %q, want %q", got, "x.ts")
	}
}

// TestAliasConfig_WalkUpStopsAtCheckout pins the boundary fix: a tsconfig
// ABOVE the checkout is not this repo's config. Before the bound, such a file
// produced a non-nil config with no paths, which suppresses the naive "@/→./"
// fallback and silently dropped every "@/" edge. An ancestor config INSIDE the
// checkout (the monorepo case) must still be found.
func TestAliasConfig_WalkUpStopsAtCheckout(t *testing.T) {
	outer := t.TempDir()
	writeFile(t, outer, "tsconfig.json", `{"compilerOptions":{}}`)

	checkout := filepath.Join(outer, "checkout")
	writeFile(t, checkout, ".git/HEAD", "ref: refs/heads/main\n")
	pkg := filepath.Join(checkout, "packages", "web")
	if err := os.MkdirAll(pkg, 0o750); err != nil {
		t.Fatal(err)
	}

	if cfg := loadAliasConfig(pkg); cfg != nil {
		t.Fatalf("config above the checkout leaked in: %#v, want nil", cfg)
	}
	if got := aliasResolve(loadAliasConfig(pkg), "@/lib/utils", "lib/utils.ts"); got != "lib/utils.ts" {
		t.Errorf("@/ fallback = %q, want %q (ancestor config must not suppress it)", got, "lib/utils.ts")
	}

	// Monorepo: a config at the checkout root still reaches a nested package.
	writeFile(t, checkout, "tsconfig.json", `{"compilerOptions":{"baseUrl":".","paths":{"@/*":["./*"]}}}`)
	cfg := loadAliasConfig(pkg)
	if cfg == nil {
		t.Fatal("checkout-root config not found from nested package, want non-nil")
	}
	if got := aliasResolve(cfg, "@/lib/utils", "lib/utils.ts"); got != "lib/utils.ts" {
		t.Errorf("monorepo @/ = %q, want %q", got, "lib/utils.ts")
	}
}

// TestBuildIndex_Rung2a_AliasResolvesToSpecificFile is the end-to-end P2 pin:
// a repo with "@/..." imports must resolve via rung 2a, not via the lossy
// repo-wide rung 5. The fixture has 2 candidates named "get" (one in the
// aliased file, one repo-wide); only rung 2a, reading the import, answers the
// aliased file.
func TestBuildIndex_Rung2a_AliasResolvesToSpecificFile(t *testing.T) {
	repo := t.TempDir()
	// tsconfig mapping "@/→./"
	writeFile(t, repo, "tsconfig.json", `{"compilerOptions":{"baseUrl":".","paths":{"@/*":["./*"]}}}`)
	// Target of alias: lib/utils.ts contains class Client with method get.
	writeFile(t, filepath.Join(repo, "lib"), "utils.ts", "export class Client {\n  static get(): void {}\n}\n")
	// Repo-wide distractor: z.ts contains class Bar with same method get.
	writeFile(t, repo, "z.ts", "export class Bar {\n  static get(): void {}\n}\n")
	// Importer uses alias and the local name Bar (alias of Client).
	writeFile(t, repo, "app.ts", "import { Client as Bar } from \"@/lib/utils\";\n\nexport function run(): void { Bar.get(); }\n")

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
	rows, err := conn.Query(`SELECT s2.file FROM edges e JOIN symbols s1 ON s1.id = e.caller JOIN symbols s2 ON s2.id = e.callee WHERE s1.name = 'run' AND s2.name = 'get'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var files []string
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("edges run()->get() land in %v, want exactly one (lib/utils.ts) — more than one means rung 2a did not narrow", files)
	}
	if files[0] != "lib/utils.ts" {
		t.Errorf("run()->get() resolved into %q, want %q: alias-aware rung 2a did not fire", files[0], "lib/utils.ts")
	}
}

// TestBuildIndex_AliasFallbackWithoutConfig verifies the naive "@/→./"
// fallback still works when NO tsconfig/jsconfig exists (covers 95%
// create-next-app trees).
func TestBuildIndex_AliasFallbackWithoutConfig(t *testing.T) {
	repo := t.TempDir()
	// No tsconfig.
	writeFile(t, filepath.Join(repo, "components/ui"), "button.tsx", "export function Button() {}\nexport function IconButton() {}\n")
	writeFile(t, filepath.Join(repo, "app"), "page.tsx", "import { Button } from \"@/components/ui/button\";\n\nexport function HomePage() { return Button(); }\n")
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
	var target string
	err = conn.QueryRow(`SELECT s2.file FROM edges e JOIN symbols s1 ON s1.id = e.caller JOIN symbols s2 ON s2.id = e.callee WHERE s1.name = 'HomePage' AND s2.name = 'Button'`).Scan(&target)
	if err != nil {
		t.Fatalf("expected edge HomePage->Button via alias fallback, query failed: %v", err)
	}
	if target != "components/ui/button.tsx" {
		t.Errorf("HomePage->Button resolved to %q, want %q (fallback alias failed)", target, "components/ui/button.tsx")
	}
}

// TestResolveSpecifier_AliasIndexAndExtensions ensures alias probing tries
// direct file, extension, and index fallbacks like relative resolution does.
func TestResolveSpecifier_AliasIndexAndExtensions(t *testing.T) {
	known := map[string]bool{
		"src/util/index.ts": true,
		"lib/utils.tsx":     true,
	}
	cfg := &aliasConfig{baseUrl: ".", paths: map[string][]string{"@/*": {"./*"}}}
	if got := resolveSpecifier("a.ts", "@/src/util", known, cfg); got != "src/util/index.ts" {
		t.Errorf("@/src/util index resolve = %q, want %q", got, "src/util/index.ts")
	}
	if got := resolveSpecifier("a.ts", "@/lib/utils", known, cfg); got != "lib/utils.tsx" {
		t.Errorf("@/lib/utils.tsx resolve = %q, want %q", got, "lib/utils.tsx")
	}
}

func TestResolveSpecifier_AliasMultiTargetFallback(t *testing.T) {
	cfg := &aliasConfig{baseUrl: ".", paths: map[string][]string{
		"@/*": {"missing/*", "found/*"},
	}}

	t.Run("later existing target wins after miss", func(t *testing.T) {
		known := map[string]bool{"found/button.tsx": true}
		if got := resolveSpecifier("app/page.tsx", "@/button", known, cfg); got != "found/button.tsx" {
			t.Errorf("resolve = %q, want %q", got, "found/button.tsx")
		}
	})
	t.Run("first existing target wins", func(t *testing.T) {
		known := map[string]bool{"missing/button.ts": true, "found/button.tsx": true}
		if got := resolveSpecifier("app/page.tsx", "@/button", known, cfg); got != "missing/button.ts" {
			t.Errorf("resolve = %q, want %q", got, "missing/button.ts")
		}
	})
	t.Run("all targets missing", func(t *testing.T) {
		if got := resolveSpecifier("app/page.tsx", "@/button", map[string]bool{}, cfg); got != "" {
			t.Errorf("resolve = %q, want empty", got)
		}
	})
}

func TestResolveSpecifier_AliasDeterministicPatternPrecedence(t *testing.T) {
	cases := []struct {
		name  string
		spec  string
		cfg   *aliasConfig
		known map[string]bool
		want  string
	}{
		{
			name: "exact key wins over wildcard",
			spec: "@/foo",
			cfg: &aliasConfig{baseUrl: ".", paths: map[string][]string{
				"@/foo": {"exact/foo"},
				"@/*oo": {"wild/*"},
			}},
			known: map[string]bool{"exact/foo.ts": true, "wild/f.ts": true},
			want:  "exact/foo.ts",
		},
		{
			name: "wildcard with longest prefix wins",
			spec: "@/bar/foo",
			cfg: &aliasConfig{baseUrl: ".", paths: map[string][]string{
				"@/*/foo": {"short/*"},
				"@/bar/*": {"long/*"},
			}},
			known: map[string]bool{"short/bar.ts": true, "long/foo.ts": true},
			want:  "long/foo.ts",
		},
		{
			name: "overlapping prefix and suffix do not match",
			spec: "@/foo",
			cfg: &aliasConfig{baseUrl: ".", paths: map[string][]string{
				"@/*/foo": {"invalid/*"},
			}},
			known: map[string]bool{},
			want:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for range 1000 {
				if got := resolveSpecifier("app/page.tsx", tc.spec, tc.known, tc.cfg); got != tc.want {
					t.Fatalf("resolve = %q, want %q", got, tc.want)
				}
			}
		})
	}
}
