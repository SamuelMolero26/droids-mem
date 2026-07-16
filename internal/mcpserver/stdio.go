package mcpserver

import (
	"context"
	"log"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/samuelmolero26/droids-mem/internal/share"
	"github.com/samuelmolero26/droids-mem/internal/state"
	"github.com/samuelmolero26/droids-mem/internal/store"
)

// bootFetchTimeout bounds the boot-Fetch git pull (PERF-2): an unreachable
// remote (suspended laptop, revoked key) must never hang the server.
const bootFetchTimeout = 15 * time.Second

// bootFetch runs the best-effort startup Fetch of the shared pool (FR-6). It is
// a no-op when no Memory repo is configured; any failure is logged and
// swallowed — a hostile or unreachable pool never blocks or crashes serve.
// Callers MUST launch this in its own goroutine, AFTER the transport is already
// accepting (PERF-1): the DB pool is single-connection, so a blocking import
// would serialize every mem_search/mem_context until it finishes.
func bootFetch(ctx context.Context, st *store.Store, logger *log.Logger) {
	repo, err := state.ShareRepoPath()
	if err != nil || repo == "" {
		return // not configured — clean no-op
	}
	ctx, cancel := context.WithTimeout(ctx, bootFetchTimeout)
	defer cancel()
	res, err := share.Fetch(ctx, st, repo)
	if err != nil {
		logger.Printf("boot fetch: %v (continuing)", err)
		return
	}
	logger.Printf("boot fetch: imported=%d skipped=%d failed=%d", res.Imported, res.Skipped, res.Failed)
}

// newMCPServer builds the MCP server with the full tool surface. Shared by
// both transports; only the instructions string differs (session-summary
// self-save policy, see instructions()).
func newMCPServer(cfg Config, st *store.Store, stdio bool) *server.MCPServer {
	s := server.NewMCPServer(ServerName, ServerVersion,
		server.WithToolCapabilities(true),
		server.WithLogging(),
		server.WithInstructions(instructions(stdio)),
	)
	registerTools(s, st)
	if cfg.Graphs != nil {
		registerGraphTools(s, cfg.Graphs)
	}
	return s
}

// RunStdio serves MCP over stdin/stdout for hosts that spawn the server as a
// child process (codex, opencode — ADR-0019). No port, no bearer token, no
// ensure-server: the pipe is private to the spawning host, and the host owns
// the lifecycle (ServeStdio returns on stdin EOF, SIGINT, or SIGTERM).
// Cfg.Addr/Endpoint/Token are ignored. The caller closes the store after
// RunStdio returns, same contract as Run.
//
// Nothing may write to stdout except the JSON-RPC stream — all logging goes
// to stderr (log.Default and WithErrorLogger both target stderr).
func RunStdio(cfg Config, st *store.Store) error {
	logger := cfg.Logger
	if logger == nil {
		logger = log.Default()
	}
	s := newMCPServer(cfg, st, true)
	logger.Printf("%s %s serving on stdio", ServerName, ServerVersion)
	// PERF-1: Fetch off the request path. ServeStdio is already ready to read
	// the JSON-RPC stream; the import races only the write lock, as any save does.
	go bootFetch(context.Background(), st, logger)
	return server.ServeStdio(s, server.WithErrorLogger(logger))
}
