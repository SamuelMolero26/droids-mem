package mcpserver

import (
	"log"

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
	logger.Printf("%s %s serving on stdio", ServerName, ServerVersion)
	return server.ServeStdio(s, server.WithErrorLogger(logger))
}
