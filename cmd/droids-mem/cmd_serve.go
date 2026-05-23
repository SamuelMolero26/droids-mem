package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/samuelmolero/droids-mem/internal/mcpserver"
	"github.com/samuelmolero/droids-mem/internal/state"
	"github.com/samuelmolero/droids-mem/internal/store"
)

func newServeCmd(s *store.Store) *cobra.Command {
	var addr, endpoint string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the MCP bridge server (Streamable HTTP + bearer auth)",
		Long: `serve starts the droids-mem MCP bridge on the configured address.
External agents (agentspan, remote droids) connect over JSON-RPC and call
mem_save / mem_search / mem_context / mem_get.

Requires DROIDS_MEM_MCP_TOKEN. Env overrides:
  DROIDS_MEM_MCP_ADDR     (default :7777)
  DROIDS_MEM_MCP_ENDPOINT (default /mcp)`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tok, err := state.LoadOrCreateToken()
			if err != nil {
				return fmt.Errorf("load token: %w", err)
			}
			cfg := mcpserver.Config{
				Addr:     envOr("DROIDS_MEM_MCP_ADDR", addr),
				Endpoint: envOr("DROIDS_MEM_MCP_ENDPOINT", endpoint),
				Token:    tok,
			}
			return mcpserver.Run(context.Background(), cfg, s)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", mcpserver.DefaultAddr, "bind address (env DROIDS_MEM_MCP_ADDR overrides)")
	cmd.Flags().StringVar(&endpoint, "endpoint", mcpserver.DefaultEndpoint, "MCP endpoint path (env DROIDS_MEM_MCP_ENDPOINT overrides)")
	return cmd
}

// envOr returns env value if set, else fallback.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
