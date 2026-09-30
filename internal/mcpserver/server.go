// Package mcpserver runs the droids-mem MCP bridge (Streamable HTTP + bearer auth).
//
// Invoked by `droids-mem serve`. logic lives in internal/store; this
// package only wires transport + auth + tool registration.
//
// Two transports, same tool surface: Run (HTTP, loopback + bearer token, for
// hosts that connect to a long-lived daemon) and RunStdio (stdin/stdout, for
// hosts that spawn the server as a child). The HTTP half carries everything
// the stdio half does not need — token, port, pid file, /identity proof,
// ensure-server — so prefer stdio for any new host.
package mcpserver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/samuelmolero26/droids-mem/internal/graph"
	"github.com/samuelmolero26/droids-mem/internal/share"
	"github.com/samuelmolero26/droids-mem/internal/state"
	"github.com/samuelmolero26/droids-mem/internal/store"
)

const (
	ShutdownGrace = 10 * time.Second
	// DefaultAddr binds loopback only. The bridge speaks plaintext HTTP with a
	// bearer token — exposing it beyond localhost requires an explicit
	// DROIDS_MEM_MCP_ADDR / --addr opt-in (and ideally a TLS-terminating proxy).
	DefaultAddr     = "127.0.0.1:7777"
	DefaultEndpoint = "/mcp"
	ServerName      = "droids-mem-mcp"
	ServerVersion   = "0.1.0"

	// maxRequestBody caps /mcp request bodies. Field caps total ~13 KB, so
	// 1 MiB leaves generous JSON-RPC envelope headroom while stopping
	// arbitrarily large bodies from being buffered pre-validation.
	maxRequestBody = 1 << 20

	// maxIdentityNonceLen bounds the /identity challenge nonce.
	maxIdentityNonceLen = 128
)

// serverInstructions is the proactive protocol surfaced to the model via the
// MCP initialize response. MCP has no auto-call primitive, so this — plus the
// per-tool descriptions — is the only cross-host lever to make an agent call
// droids-mem on its own; hard enforcement stays a per-host hook concern.
//
// Budget, measured (TestInstructions_Budget): Claude Code hands the model only
// the first 2048 chars, and Codex relies on the first 512 standing alone. So
// the numbered core loop comes first and fits in 512, and the whole text fits
// in 2048 — a rule past the cut never reaches the model on any Claude Code
// surface. One text for every transport: the summary rule tells a hooked host
// that staging counts, and dedupe absorbs a redundant save.
const serverInstructions = `droids-mem is your persistent memory and code graph. Use it on your own, without being asked:
1. At task start and on topic shifts: mem_search a short description of the task.
2. Code in Go/Python/TS/JS: graph_package to orient, graph_symbol for source + callers/callees, before grep or reading files. Before editing a function: graph_symbol direction=up depth=3; cite transitive_callers.
3. Learned something reusable: mem_save it.
4. End of a run: save ONE session_summary.

SEARCH: results are ranked previews; mem_get an id for the full body before relying on it. all_projects=true searches every repo. For continuity also call mem_context with task_type = the git repo name, reused verbatim every session.

SAVE: kind error_resolution (problem + fix), task_pattern (repeatable approach) or user_rule (a user correction or preference). Only reusable lessons; re-saving is harmless (deduped). Reuse the session_id from the first mem_context or mem_save on every later call in the run.

SUMMARY: when the task is done or the user wraps up, mem_save kind=session_summary: what happened, what you learned, what comes next. Once per run; if a host hook already asked you to stage one, that counts.

GRAPH: re-query a stub's exact qname to expand it; 'to' returns a call path; pass the project root as 'repo'. freshness.stale: true means the whole previous index is served; carried: true or stale_units means that package rides on old edges. Mapper-tier (Python/TS/JS) edges are heuristic. Verify critical findings against source.

After a memory or graph call, say in one line what it told you, e.g. "Store.Save has 15 transitive callers, keeping its signature".

Never put secrets, API keys or tokens in any field, tags included: tags are stored unscrubbed.`

// Config controls the MCP bridge server. Zero values fall back to defaults.
type Config struct {
	Addr     string // e.g. ":7777"
	Endpoint string // e.g. "/mcp"
	Token    string // required bearer token; Run errors if empty
	Logger   *log.Logger
	Graphs   *graph.Manager // optional code-graph subsystem (ADR-0020); nil skips graph tools
	// Version is the binary's build-time release version, advertised on
	// /identity so ensure-server can replace a daemon running other code.
	Version string
}

// Run starts the MCP bridge and blocks until ctx is canceled or the server
// exits. The caller owns the *store.Store (and the underlying DB) so it can
// close them after Run returns — guaranteeing no writer txn is killed
// mid-flight by the deferred Close.
func Run(ctx context.Context, cfg Config, st *store.Store) error {
	if cfg.Token == "" {
		return errors.New("DROIDS_MEM_MCP_TOKEN is required (bearer auth)")
	}
	if cfg.Addr == "" {
		cfg.Addr = DefaultAddr
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = DefaultEndpoint
	}
	logger := cfg.Logger
	if logger == nil {
		logger = log.Default()
	}

	s := newMCPServer(cfg, st)

	mcpHandler := server.NewStreamableHTTPServer(s,
		server.WithEndpointPath(cfg.Endpoint),
		server.WithHeartbeatInterval(30*time.Second),
	)

	mux := http.NewServeMux()
	mux.Handle(cfg.Endpoint, mcpHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/identity", identityHandler(cfg.Token, cfg.Version))
	// The graph viewer is loopback-only: never mounted on a wider bind.
	if cfg.Graphs != nil && IsLoopbackAddr(cfg.Addr) {
		registerUI(mux, cfg.Token, cfg.Graphs)
	}

	wrapped := bearerAuth(cfg.Token, cfg.Endpoint, limitBody(mux))

	if host, _, err := net.SplitHostPort(cfg.Addr); err == nil && !isLoopbackHost(host) {
		logger.Printf("WARNING: binding non-loopback address %q without TLS — bearer token and memory content travel in plaintext", cfg.Addr)
	}

	logger.Printf("%s %s listening on %s (endpoint=%s)", ServerName, ServerVersion, cfg.Addr, cfg.Endpoint)
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           wrapped,
		ReadHeaderTimeout: 10 * time.Second,
		// No ReadTimeout/WriteTimeout: Streamable HTTP holds long-lived
		// response streams with 30 s heartbeats; blanket timeouts would
		// sever healthy sessions. IdleTimeout only reaps dead keep-alives.
		IdleTimeout: 2 * time.Minute,
	}

	stopCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	// Boot auto-Fetch (ADR-0029 §5): pull a teammate's shared pool once, in its
	// own goroutine launched after the listener is already serving, so this
	// detached server's single DB connection is never blocked at startup.
	// stopCtx (not ctx) so a SIGTERM cancels an in-flight fetch before the
	// caller's deferred db.Close runs.
	// Waited on before returning: canceling stopCtx only *asks* the fetch to
	// stop, it does not wait for it. Returning while the goroutine is still
	// mid-import would let the caller's deferred db.Close land under a live
	// write — exactly the guarantee this function's doc comment makes.
	var fetchWG sync.WaitGroup
	startBootFetch(stopCtx, st, logger, &fetchWG)
	defer fetchWG.Wait()

	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	case <-stopCtx.Done():
		logger.Printf("shutdown signal received; draining (grace=%s)", ShutdownGrace)
		shutCtx, cancel := context.WithTimeout(context.Background(), ShutdownGrace)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			logger.Printf("graceful shutdown failed: %v (forcing close)", err)
			_ = srv.Close()
		}
		logger.Printf("shutdown complete")
		return nil
	}
}

// bootFetchTimeout bounds the boot auto-Fetch git pull so an unreachable remote
// can't hang and pin the single DB connection (ADR-0029 §5).
const bootFetchTimeout = 15 * time.Second

// startBootFetch runs one best-effort Fetch of the configured shared pool in its
// own goroutine (ADR-0029 §5) so a teammate's memories reach this agent without
// a human remembering to pull. No repo configured → no-op. Failure is logged,
// never fatal — a stale or unreachable pool must not degrade serving. The
// caller owns wg and must Wait on it before releasing the store.
func startBootFetch(ctx context.Context, st *store.Store, logger *log.Logger, wg *sync.WaitGroup) {
	repo := shareRepo()
	if repo == "" {
		return
	}
	wg.Go(func() {
		fctx, cancel := context.WithTimeout(ctx, bootFetchTimeout)
		defer cancel()
		res, err := share.Fetch(fctx, repo, st)
		if err != nil {
			logger.Printf("boot fetch skipped: %v", err)
			return
		}
		logger.Printf("boot fetch: imported %d, skipped %d, failed %d from %s",
			res.Imported, res.Skipped, res.Failed, repo)
	})
}

// shareRepo resolves the shared-pool repo path: DROIDS_MEM_SHARE_REPO overrides
// (tests/one-offs), else the persisted ~/.droids-mem/share_repo. A detached
// serve does not inherit the user's shell env, so the file is the load-bearing
// source (ADR-0029 §6); the env var is the escape hatch.
func shareRepo() string {
	if v := os.Getenv("DROIDS_MEM_SHARE_REPO"); v != "" {
		return v
	}
	repo, _ := state.LoadShareRepo()
	return repo
}

// identityHandler answers a challenge–response proof of token knowledge:
// GET /identity?nonce=<client nonce> → {"server", "proof", "version", "pid", "pid_proof"}.
// Unauthenticated by design — the proofs reveal nothing about the token, and
// they let ensure-server verify that whatever answers on this port actually
// holds the shared token before reporting "already_running" (anti
// port-squatting). A fresh client nonce per check makes replay useless.
//
// "proof" answers "does this listener hold the token"; "pid_proof" additionally
// answers "which process is it", which a caller about to send a signal needs
// and the token alone cannot establish.
func identityHandler(token, version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nonce := r.URL.Query().Get("nonce")
		if nonce == "" || len(nonce) > maxIdentityNonceLen {
			http.Error(w, `{"error":"nonce required"}`, http.StatusBadRequest)
			return
		}
		pid := os.Getpid()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"server":%q,"proof":%q,"version":%q,"pid":%d,"pid_proof":%q}`,
			ServerName, IdentityProof(token, nonce), version, pid,
			IdentityPidProof(token, nonce, pid))
	}
}

// IdentityProof computes the expected /identity response proof for a given
// token + nonce. Shared with ensure-server so client and server can never
// drift on the HMAC construction.
func IdentityProof(token, nonce string) string {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte(nonce))
	return hex.EncodeToString(mac.Sum(nil))
}

// IdentityPidProof binds the PID into a proof under a token-derived key.
// Separate from IdentityProof so older daemons still pass ensure-server;
// derived key so a plain proof of nonce "N:pid" can't double as this one.
func IdentityPidProof(token, nonce string, pid int) string {
	kmac := hmac.New(sha256.New, []byte(token))
	kmac.Write([]byte("droids-mem/pid_proof"))
	mac := hmac.New(sha256.New, kmac.Sum(nil))
	mac.Write([]byte(nonce + ":" + strconv.Itoa(pid)))
	return hex.EncodeToString(mac.Sum(nil))
}

// limitBody caps request bodies before they reach JSON-RPC decoding.
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
		next.ServeHTTP(w, r)
	})
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// bearerAuth gates the MCP endpoint with a constant-time bearer-token compare.
// /healthz is intentionally exempt so liveness probes do not need credentials.
func bearerAuth(expected, protectedPath string, next http.Handler) http.Handler {
	want := []byte("Bearer " + expected)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != protectedPath {
			next.ServeHTTP(w, r)
			return
		}
		got := []byte(r.Header.Get("Authorization"))
		if len(got) != len(want) || subtle.ConstantTimeCompare(got, want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="droids-mem-mcp"`)
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
