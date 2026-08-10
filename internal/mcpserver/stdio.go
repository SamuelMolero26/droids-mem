package mcpserver

import (
	"context"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/mark3labs/mcp-go/server"

	"github.com/samuelmolero26/droids-mem/internal/store"
)

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

	// Boot auto-Fetch (ADR-0029 §5) runs here too, or the shared pool silently
	// stops arriving for every stdio host. Deliberately per spawn rather than
	// throttled: this is 1-19 sessions a day in practice, and a fetch against
	// an unreachable remote costs ~2s of teardown now that runGit bounds its
	// child pipes — a timer would be machinery for a cost that is not there.
	//
	// Waited on before returning so the caller's store outlives an in-flight
	// import, exactly as Run does. A session that ends first cancels it; the
	// import is per-row and idempotent, so a partial pool is safe and the next
	// spawn picks up where this one stopped.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var fetchWG sync.WaitGroup
	startBootFetch(ctx, st, logger, &fetchWG)
	defer fetchWG.Wait()

	logger.Printf("%s %s serving on stdio", ServerName, ServerVersion)
	return server.ServeStdio(s, server.WithErrorLogger(logger))
}
