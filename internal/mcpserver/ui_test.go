package mcpserver

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/samuelmolero26/droids-mem/internal/graph"
)

func TestVerifyUIKey(t *testing.T) {
	const token, repo = "tok", "/repo/a"
	now := time.Unix(1_800_000_000, 0)
	valid := UIKey(token, repo, now.Add(time.Hour))
	parts := strings.Split(valid, ".")
	// Same MAC, repo swapped: a key must not be repointable at another repo.
	repointed := strings.Join([]string{parts[0], parts[1], b64.EncodeToString([]byte("/repo/b")), parts[3]}, ".")

	cases := []struct {
		name  string
		token string
		key   string
		ok    bool
	}{
		{"valid", token, valid, true},
		{"expired", token, UIKey(token, repo, now.Add(-time.Second)), false},
		{"expiry beyond TTL", token, UIKey(token, repo, now.Add(UIKeyTTL+2*uiKeySkew)), false},
		{"expiry within skew", token, UIKey(token, repo, now.Add(UIKeyTTL+uiKeySkew/2)), true},
		{"wrong token", "other", valid, false},
		{"repointed repo", token, repointed, false},
		{"wrong version", token, "v2" + strings.TrimPrefix(valid, "v1"), false},
		{"too few parts", token, "v1.1.2", false},
		{"bad base64", token, parts[0] + "." + parts[1] + ".!!." + parts[3], false},
		{"empty", token, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := verifyUIKey(tc.token, tc.key, now)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && got != repo {
				t.Fatalf("repo = %q, want %q", got, repo)
			}
		})
	}
}

// Package and symbol names come from the indexed repo, so a package named
// constructor or toString must not hit Object.prototype through a `{}` dict.
func TestUIAssets_NoPrototypeDicts(t *testing.T) {
	dict := regexp.MustCompile(`=\s*\{\}`)
	files, err := fs.Glob(uiAssets, "ui/*.js")
	if err != nil || len(files) == 0 {
		t.Fatalf("glob ui/*.js: %v (%d files)", err, len(files))
	}
	for _, f := range files {
		b, err := uiAssets.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if dict.MatchString(line) {
				t.Errorf("%s:%d uses a {} dict; use Object.create(null): %s", f, i+1, strings.TrimSpace(line))
			}
		}
	}
}

func TestUIGuard(t *testing.T) {
	cases := []struct {
		name, host, origin string
		want               int
	}{
		{"ipv4 loopback", "127.0.0.1:7777", "", http.StatusOK},
		{"localhost", "localhost:7777", "", http.StatusOK},
		{"ipv6 loopback", "[::1]:7777", "", http.StatusOK},
		{"no port", "localhost", "", http.StatusOK},
		{"rebinding host", "evil.example:7777", "", http.StatusForbidden},
		{"lan host", "192.168.1.5:7777", "", http.StatusForbidden},
		{"loopback origin", "127.0.0.1:7777", "http://localhost:7777", http.StatusOK},
		{"cross-site origin", "127.0.0.1:7777", "https://evil.example", http.StatusForbidden},
		{"null origin", "127.0.0.1:7777", "null", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/ui/", nil)
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			uiGuard(okHandler).ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			// Hardening headers go out on every response, rejected ones included.
			if rec.Header().Get("Content-Security-Policy") != uiCSP {
				t.Fatalf("CSP header missing")
			}
		})
	}
}

// uiFixture indexes a tiny TS repo with a six-deep call chain a→b→c→d→e→f and
// mounts the viewer on a fresh mux.
func uiFixture(t *testing.T) (mux *http.ServeMux, token, repo string) {
	t.Helper()
	repo = t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	src := `export function f() { return 1; }
export function e() { return f(); }
export function d() { return e(); }
export function c() { return d(); }
export function b() { return c(); }
export function a() { return b(); }
`
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "src", "chain.ts"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	gm := graph.NewManager(filepath.Join(t.TempDir(), "graphs"))
	t.Cleanup(gm.Close)
	if _, err := gm.Index(context.Background(), repo); err != nil {
		t.Fatalf("Index: %v", err)
	}
	token = "tok"
	mux = http.NewServeMux()
	registerUI(mux, token, gm)
	return mux, token, repo
}

func uiGet(t *testing.T, mux *http.ServeMux, path, key string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = "127.0.0.1:7777"
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestUIAPI(t *testing.T) {
	mux, token, repo := uiFixture(t)
	key := UIKey(token, repo, time.Now().Add(time.Hour))

	var ov graph.OverviewResponse
	rec := uiGet(t, mux, "/api/graph/overview", key)
	if rec.Code != http.StatusOK {
		t.Fatalf("overview status = %d: %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ov); err != nil || len(ov.Packages) == 0 {
		t.Fatalf("overview = %s (err %v), want packages", rec.Body, err)
	}

	cases := []struct {
		name, path, key string
		want            int
	}{
		{"shell is public", "/ui/", "", http.StatusOK},
		{"no key", "/api/graph/overview", "", http.StatusUnauthorized},
		{"key for another repo", "/api/graph/overview", UIKey(token, t.TempDir(), time.Now().Add(time.Hour)), http.StatusNotFound},
		{"package", "/api/graph/package?package=" + url.QueryEscape(ov.Packages[0].Name), key, http.StatusOK},
		{"package missing param", "/api/graph/package", key, http.StatusBadRequest},
		{"search", "/api/graph/search?q=chain", key, http.StatusOK},
		{"search too short", "/api/graph/search?q=a", key, http.StatusBadRequest},
		{"entrypoints", "/api/graph/entrypoints", key, http.StatusOK},
		{"symbol", "/api/graph/symbol?symbol=f", key, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if rec := uiGet(t, mux, tc.path, tc.key); rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
		})
	}
}

// The viewer must walk as deep as the graph_symbol MCP tool does (max 5), not
// stop short of it.
func TestUIAPI_SymbolDepthMatchesMCP(t *testing.T) {
	mux, token, repo := uiFixture(t)
	key := UIKey(token, repo, time.Now().Add(time.Hour))

	rec := uiGet(t, mux, "/api/graph/symbol?symbol=f&direction=up&depth=5", key)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var resp graph.SymbolResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	deepest := 0
	for _, n := range resp.Callers {
		deepest = max(deepest, n.Depth)
	}
	if deepest != 5 {
		t.Fatalf("deepest caller depth = %d, want 5 (callers %+v)", deepest, resp.Callers)
	}
}
