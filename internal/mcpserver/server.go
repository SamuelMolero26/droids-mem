// Package mcpserver runs the droids-mem MCP bridge (Streamable HTTP + bearer auth).
//
// Invoked by `droids-mem serve`. logic lives in internal/store; this
// package only wires transport + auth + tool registration.
//
// Architecture rationale: docs/adr/0003-mcp-bridge-for-agentspan.md.
package mcpserver

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/samuelmolero/droids-mem/internal/store"
)

const (
	ShutdownGrace   = 10 * time.Second
	DefaultAddr     = ":7777"
	DefaultEndpoint = "/mcp"
	ServerName      = "droids-mem-mcp"
	ServerVersion   = "0.1.0"
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

	wrapped := bearerAuth(cfg.Token, cfg.Endpoint, mux)

	logger.Printf("%s %s listening on %s (endpoint=%s)", ServerName, ServerVersion, cfg.Addr, cfg.Endpoint)
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           wrapped,
		ReadHeaderTimeout: 10 * time.Second,
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
