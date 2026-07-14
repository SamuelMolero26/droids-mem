package mcpserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/samuelmolero26/droids-mem/internal/graph"
)

// registerGraphTools exposes the native code-graph subsystem (ADR-0020) as
// two MCP tools mirroring the two shapes of real code questions: symbol-
// anchored and scope-anchored. Both are signatures-first: neighbors come back
// as one-line stubs, bodies only for the symbol asked about by exact name.
func registerGraphTools(s *server.MCPServer, gm *graph.Manager) {
	s.AddTool(graphSymbolToolDef(), mcp.NewTypedToolHandler(graphSymbolHandler(gm)))
	s.AddTool(graphPackageToolDef(), mcp.NewTypedToolHandler(graphPackageHandler(gm)))
}

// ---------- graph_symbol ----------

type graphSymbolArgs struct {
	Repo      string `json:"repo"`
	Symbol    string `json:"symbol"`
	Direction string `json:"direction,omitempty"`
	Depth     int    `json:"depth,omitempty"`
	To        string `json:"to,omitempty"`
}

func graphSymbolToolDef() mcp.Tool {
	return mcp.NewTool("graph_symbol",
		mcp.WithDescription("Query the code graph of a Go repo anchored on one symbol — use this INSTEAD of grep/file-reading to understand code. Returns the symbol's full source plus its callers/callees as one-line signature stubs (interface dispatch resolved) and 'transitive_callers': the blast size (how many symbols transitively call it) so you know if a change is risky before walking it. depth>1 with direction=up lists that blast radius; 'to' gives the call path between two symbols. To read a stub's body, call again with its exact qname. SEARCH FALLBACK: if 'symbol' does not resolve to a name, it is treated as a task phrase and you get a relevance-ranked 'matches' menu of signatures — re-query with one of their qnames for full context. The graph auto-rebuilds when the repo changed; a 'stale' freshness flag means the repo currently does not compile and the last good graph is being served."),
		mcp.WithString("repo", mcp.Required(),
			mcp.Description("Absolute path to the repo root (your project working directory).")),
		mcp.WithString("symbol", mcp.Required(),
			mcp.Description("A symbol name ('Save'), receiver-qualified ('Store.Save'), or exact qname ('internal/store.Store.Save') — OR a free-text task phrase ('dedupe race on save') to search when you don't know the name.")),
		mcp.WithString("direction",
			mcp.Description("Which edges to follow: up (callers — who depends on this), down (callees — what this uses), both (default)."),
			mcp.Enum("up", "down", "both"),
		),
		mcp.WithNumber("depth",
			mcp.Description("Transitive hops to walk (default 1, max 5). Use direction=up with depth 3-5 for 'what breaks if I change this'."),
			mcp.DefaultNumber(1), mcp.Min(1), mcp.Max(5),
		),
		mcp.WithString("to",
			mcp.Description("Optional target symbol: returns the shortest call path from 'symbol' to 'to' instead of neighbors.")),
	)
}

func graphSymbolHandler(gm *graph.Manager) func(context.Context, mcp.CallToolRequest, graphSymbolArgs) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, _ mcp.CallToolRequest, a graphSymbolArgs) (*mcp.CallToolResult, error) {
		resp, err := gm.Symbol(ctx, graph.SymbolRequest{
			Repo:      a.Repo,
			Symbol:    a.Symbol,
			Direction: a.Direction,
			Depth:     a.Depth,
			To:        a.To,
		})
		if err != nil {
			return graphToolErr(err), nil
		}
		return mcp.NewToolResultText(graph.RenderSymbol(resp)), nil
	}
}

// ---------- graph_package ----------

type graphPackageArgs struct {
	Repo    string `json:"repo"`
	Package string `json:"package"`
}

func graphPackageToolDef() mcp.Tool {
	return mcp.NewTool("graph_package",
		mcp.WithDescription("Get the public surface of one Go package — exported symbols as one-line signatures with first doc lines, never bodies. Use this to orient in an area of a repo before drilling into symbols with graph_symbol. Same auto-rebuild and staleness semantics as graph_symbol."),
		mcp.WithString("repo", mcp.Required(),
			mcp.Description("Absolute path to the repo root (your project working directory).")),
		mcp.WithString("package", mcp.Required(),
			mcp.Description("Package path or suffix, e.g. 'internal/store' or just 'store'.")),
	)
}

func graphPackageHandler(gm *graph.Manager) func(context.Context, mcp.CallToolRequest, graphPackageArgs) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, _ mcp.CallToolRequest, a graphPackageArgs) (*mcp.CallToolResult, error) {
		resp, err := gm.Package(ctx, graph.PackageRequest{Repo: a.Repo, Package: a.Package})
		if err != nil {
			return graphToolErr(err), nil
		}
		return mcp.NewToolResultText(graph.RenderPackage(resp)), nil
	}
}

// graphToolErr keeps not-found errors structured so the agent can distinguish
// a miss (retry with a different name) from a runtime failure.
func graphToolErr(err error) *mcp.CallToolResult {
	if errors.Is(err, graph.ErrNotFound) {
		return mcp.NewToolResultError(fmt.Sprintf(`{"status":"error","error":"not_found","message":%q,"retryable":true,"suggestion":"check spelling, or query graph_package first to list symbols"}`, err.Error()))
	}
	return mcp.NewToolResultError(err.Error())
}
