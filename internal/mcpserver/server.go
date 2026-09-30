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

// The instructions string is the proactive protocol surfaced to the model via
// the MCP initialize response (ADR-0019, Layer 1). MCP has no auto-call
// primitive, so this — plus the per-tool descriptions — is the only cross-host
// lever to make an agent call droids-mem on its own. It is best-effort: the
// floor is model judgment, backstopped by the store's dedup. Hard enforcement
// stays a per-host hook concern (ADR-0016 is the Claude Code adapter).
//
// Only the session-summary sentence differs by transport: HTTP hosts (Claude
// Code, ADR-0016) have hooks that record summaries automatically, so the model
// must NOT double-save; stdio hosts (codex, opencode) have no such hook wired
// by default, so the model must save one itself or the run leaves no
// continuity. A host that does wire a flush hook is still safe — dedupe makes
// the redundant self-save harmless.
const instructionsCore = `droids-mem is your persistent memory across sessions: prior fixes, decisions, and conventions, so you do not relearn them. Call these tools on your own; do not wait to be asked.

AT THE START of a task, and whenever the topic shifts:
- mem_search with a short description of the task. Results are ranked; read the full body with mem_get before relying on one. Each row has authored_at (unix seconds, when the lesson was written) — treat an old lesson about fast-moving code as possibly stale and verify it against current code.
- mem_context with task_type = the repo or top-level directory name, the exact same string every session, for the last session summary and standing user rules.

AS YOU WORK, mem_save each genuinely reusable lesson (not routine steps): error_resolution (problem + fix that worked), task_pattern (repeatable approach), user_rule (a correction or preference the user gave). The store deduplicates, so prefer saving over forgetting. Reuse one session_id for the whole run.

AFTER EACH droids-mem call, say in one line what you learned and how it changes your approach (e.g. "graph_symbol: Store.Save has 15 transitive callers — keeping its signature"), so the user can see decisions come from memory and graph data.

FOR CODE in a Go, Python, TypeScript, or JavaScript repo, prefer graph_package (orient) and graph_symbol (one symbol + callers/callees) over grep and file reads. Pass the project root as repo.
`

const summaryPolicyHTTP = `Do NOT save session summaries here — your host records them at session end; saving one yourself would duplicate it.`

const summaryPolicyStdio = `AT THE END of a run, save ONE session_summary (what happened, what you learned, what comes next). No hook on this host records it for you.`

const instructionsTail = `Never put secrets, tokens, or keys in any field. Tags are stored unscrubbed.

BEFORE EDITING a function, call graph_symbol with direction=up depth=3 and mention its transitive_callers count — the blast radius of the change. Python/TS/JS (mapper-tier) callers are approximate; a stale or carried graph answer means verify against source before acting.`

// instructions assembles the transport-appropriate protocol string. It carries
// only when and why to call; parameter and output detail lives in the tool
// descriptions, which every host already sends, so it is never repeated here.
func instructions(stdio bool) string {
	if stdio {
		return instructionsCore + "\n" + summaryPolicyStdio + "\n\n" + instructionsTail
	}
	return instructionsCore + "\n" + summaryPolicyHTTP + "\n\n" + instructionsTail
}

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

	s := newMCPServer(cfg, st, false)

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
// GET /identity?nonce=<client nonce> → {"server", "proof", "version", "pid", "pid_proof", "ui"}.
// Unauthenticated by design — the proofs reveal nothing about the token, and
// they let ensure-server verify that whatever answers on this port actually
// holds the shared token before reporting "already_running" (anti
// port-squatting). A fresh client nonce per check makes replay useless.
//
// "proof" answers "does this listener hold the token"; "pid_proof" additionally
// answers "which process is it", which a caller about to send a signal needs
// and the token alone cannot establish.
//
// "ui" says this build ships the graph viewer. Local builds all report version
// "dev", so it is what tells ensure-server that a daemon left running from an
// older checkout lacks /ui/. It is a property of the build, not of whether the
// viewer is mounted on this bind, so a non-loopback daemon is not replaced on
// every call.
func identityHandler(token, version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nonce := r.URL.Query().Get("nonce")
		if nonce == "" || len(nonce) > maxIdentityNonceLen {
			http.Error(w, `{"error":"nonce required"}`, http.StatusBadRequest)
			return
		}
		pid := os.Getpid()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"server":%q,"proof":%q,"version":%q,"pid":%d,"pid_proof":%q,"ui":true}`,
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
