package mcpserver

// Loopback-only graph viewer: static shell under /ui/ and a read-only JSON API
// under /api/graph/. It is registered only when the daemon binds a loopback
// address, and never serves /mcp, memory data, or any repo but the one named in
// the caller's signed key.

import (
	"crypto/hmac"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/samuelmolero26/droids-mem/internal/graph"
)

//go:embed ui
var uiAssets embed.FS

const (
	// UIKeyTTL is how long a key minted by `graph ui` stays valid.
	UIKeyTTL = 12 * time.Hour
	// uiKeySkew tolerates clock drift between the minting CLI and the daemon.
	uiKeySkew = time.Minute

	uiCSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'"
)

var b64 = base64.RawURLEncoding

// IsLoopbackAddr reports whether a bind address (host:port) is loopback-only.
// An empty host (":7777") binds every interface and is not loopback.
func IsLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	return err == nil && isLoopbackHost(host)
}

// uiMACKey derives the UI signing key from the bearer token. It must not be
// the token itself: /identity hands any caller HMAC(token, nonce) for a nonce
// of their choosing, so a UI key MACed directly with the token would be
// forgeable from that oracle. The derivation label differs from every label
// /identity uses, so nothing it returns is a UI key.
func uiMACKey(token string) []byte {
	h := sha256.Sum256([]byte("droids-mem/ui-key/v1\x00" + token))
	return h[:]
}

func uiKeyMAC(token, exp, repo string) []byte {
	mac := hmac.New(sha256.New, uiMACKey(token))
	mac.Write([]byte("v1\n" + exp + "\n" + repo))
	return mac.Sum(nil)
}

// UIKey mints a key granting read access to repo's graph until exp:
// v1.<exp unix>.<b64 repo>.<b64 mac>. The repo rides inside the signed claim,
// so the API needs no repo parameter and a key cannot be repointed.
func UIKey(token, repo string, exp time.Time) string {
	e := strconv.FormatInt(exp.Unix(), 10)
	return "v1." + e + "." + b64.EncodeToString([]byte(repo)) + "." + b64.EncodeToString(uiKeyMAC(token, e, repo))
}

// verifyUIKey returns the repo a valid, unexpired key is scoped to. Keys
// expiring further out than UIKeyTTL are rejected: the CLI never mints them.
func verifyUIKey(token, key string, now time.Time) (string, bool) {
	parts := strings.Split(key, ".")
	if len(parts) != 4 || parts[0] != "v1" {
		return "", false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || now.Unix() > exp || exp > now.Add(UIKeyTTL+uiKeySkew).Unix() {
		return "", false
	}
	repoB, err := b64.DecodeString(parts[2])
	if err != nil {
		return "", false
	}
	mac, err := b64.DecodeString(parts[3])
	if err != nil {
		return "", false
	}
	if !hmac.Equal(mac, uiKeyMAC(token, parts[1], string(repoB))) { // constant-time
		return "", false
	}
	return string(repoB), true
}

// registerUI mounts the viewer on mux.
func registerUI(mux *http.ServeMux, token string, gm *graph.Manager) {
	assets, _ := fs.Sub(uiAssets, "ui") // the embedded dir always exists
	mux.Handle("GET /ui/", uiGuard(http.StripPrefix("/ui/", http.FileServerFS(assets))))

	api := func(name string, h func(r *http.Request, repo string) (any, error)) {
		mux.Handle("GET /api/graph/"+name, uiGuard(uiKeyed(token, h)))
	}
	api("overview", func(r *http.Request, repo string) (any, error) {
		return gm.PackageOverview(r.Context(), repo)
	})
	api("package", func(r *http.Request, repo string) (any, error) {
		return gm.PackageSymbols(r.Context(), repo, r.URL.Query().Get("package"))
	})
	api("search", func(r *http.Request, repo string) (any, error) {
		return gm.SearchSymbols(r.Context(), repo, r.URL.Query().Get("q"))
	})
	api("entrypoints", func(r *http.Request, repo string) (any, error) {
		return gm.EntryPoints(r.Context(), repo)
	})
	api("symbol", func(r *http.Request, repo string) (any, error) {
		q := r.URL.Query()
		depth, _ := strconv.Atoi(q.Get("depth"))
		dir := q.Get("direction")
		if dir != "up" && dir != "down" {
			dir = "both"
		}
		// Symbol clamps depth to 1..5, the same bound graph_symbol gets.
		return gm.Symbol(r.Context(), graph.SymbolRequest{
			Repo: repo, Symbol: q.Get("symbol"), Direction: dir,
			Depth: depth, NoBuild: true,
		})
	})
}

// uiGuard sets the hardening headers and rejects any request whose Host or
// Origin is not loopback (DNS rebinding and cross-site reads), before any
// handler or key check runs.
func uiGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", uiCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		if !loopbackHostHeader(r.Host) {
			http.Error(w, `{"error":"forbidden host"}`, http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || !isLoopbackHost(u.Hostname()) {
				http.Error(w, `{"error":"forbidden origin"}`, http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func loopbackHostHeader(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = strings.Trim(hostport, "[]") // no port
	}
	return isLoopbackHost(host)
}

// uiKeyed requires a valid UI key, runs h against the repo the key names, and
// writes its result as JSON.
func uiKeyed(token string, h func(r *http.Request, repo string) (any, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			writeUIError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		repo, ok := verifyUIKey(token, bearer, time.Now())
		if !ok {
			writeUIError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		out, err := h(r, repo)
		switch {
		case errors.Is(err, graph.ErrNotFound):
			writeUIError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, graph.ErrInvalidArgument):
			writeUIError(w, http.StatusBadRequest, err.Error())
		case err != nil:
			writeUIError(w, http.StatusInternalServerError, err.Error())
		default:
			_ = json.NewEncoder(w).Encode(out)
		}
	})
}

func writeUIError(w http.ResponseWriter, status int, msg string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
