// Package mcpserver runs the droids-mem MCP bridge (Streamable HTTP + bearer auth).
//
// Invoked by `droids-mem serve`. logic lives in internal/store; this
// package only wires transport + auth + tool registration.
//
// Architecture rationale: docs/adr/0003-mcp-bridge-for-agentspan.md.
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
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/samuelmolero/droids-mem/internal/store"
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

// Config controls the MCP bridge server. Zero values fall back to defaults.
type Config struct {
	Addr     string // e.g. ":7777"
	Endpoint string // e.g. "/mcp"
	Token    string // required bearer token; Run errors if empty
	Logger   *log.Logger
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

	s := server.NewMCPServer(ServerName, ServerVersion,
		server.WithToolCapabilities(true),
		server.WithLogging(),
	)
	registerTools(s, st)

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
	mux.HandleFunc("/identity", identityHandler(cfg.Token))

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

// identityHandler answers a challenge–response proof of token knowledge:
// GET /identity?nonce=<client nonce> → {"server":..., "proof": hex(HMAC-SHA256(token, nonce))}.
// Unauthenticated by design — the proof reveals nothing about the token, and it
// lets ensure-server verify that whatever answers on this port actually holds
// the shared token before reporting "already_running" (anti port-squatting).
// A fresh client nonce per check makes replay of old proofs useless.
func identityHandler(token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nonce := r.URL.Query().Get("nonce")
		if nonce == "" || len(nonce) > maxIdentityNonceLen {
			http.Error(w, `{"error":"nonce required"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"server":%q,"proof":%q}`, ServerName, IdentityProof(token, nonce))
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
