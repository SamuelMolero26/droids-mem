package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/samuelmolero26/droids-mem/internal/graph"
	"github.com/samuelmolero26/droids-mem/internal/state"
)

// registerGraphTools exposes the native code-graph subsystem (ADR-0020) as
// two MCP tools mirroring the two shapes of real code questions: symbol-
// anchored and scope-anchored. Both are signatures-first: neighbors come back
// as one-line stubs, bodies only for the symbol asked about by exact name.
func registerGraphTools(s *server.MCPServer, gm *graph.Manager) {
	s.AddTool(graphSymbolToolDef(), mcp.NewTypedToolHandler(graphSymbolHandler(gm)))
	s.AddTool(graphPackageToolDef(), mcp.NewTypedToolHandler(graphPackageHandler(gm)))
	s.AddTool(graphBuildWaitToolDef(), mcp.NewTypedToolHandler(graphBuildWaitHandler(gm)))
}

// ---------- graph_symbol ----------

type graphSymbolArgs struct {
	Repo      string  `json:"repo"`
	Symbol    string  `json:"symbol"`
	Direction string  `json:"direction,omitempty"`
	Depth     float64 `json:"depth,omitempty"`
	NoSource  bool    `json:"no_source,omitempty"`
	NoTests   bool    `json:"no_tests,omitempty"`
	To        string  `json:"to,omitempty"`
}

func graphSymbolToolDef() mcp.Tool {
	return mcp.NewTool("graph_symbol",
		mcp.WithDescription(`Code graph around one symbol in a Go (type-resolved) or Python/TS/JS (syntactic; see hint) repo; use instead of grep or reading files. Returns its source, callers/callees as one-line stubs (interface dispatch resolved), and transitive_callers, the count of transitive callers (funcs/methods only; for a type/const/var the hint says how to gauge dependents). An interface lists 'implementers' (+ implementers_total), the set a method-signature change must update; a concrete type lists the interfaces it 'satisfies'. Re-query a stub's qname for its body. An unresolved 'symbol' is treated as a task phrase and returns a ranked 'matches' menu of qnames. Rebuilds automatically when the repo changes. A call into a closure is not an edge (a callback is never a caller of the function receiving it); a call inside a closure belongs to the enclosing declaration.

Before editing: direction=up depth=3; transitive_callers > 10 means add a layer or keep backward compat. Before reading: direction=down depth=1, then drill callees.

DEGRADED: freshness.stale = genuine build failure (I/O, corrupt cache, or over half the packages broken); the last good index is served whole. A package failing to type-check degrades alone: 'carried': true = the symbol's package uses the previous build's edges; freshness.stale_units names those packages (capped; see stale_units_total). freshness.index_error says why a rebuild failed. Verify critical degraded findings in source.`),
		mcp.WithString("repo", mcp.Required(),
			mcp.Description("Absolute path to the project root.")),
		mcp.WithString("symbol", mcp.Required(),
			mcp.Description("Name ('Save'), receiver-qualified ('Store.Save'), or qname ('internal/store.Store.Save'); or a task phrase ('dedupe race on save') when the name is unknown.")),
		mcp.WithString("direction",
			mcp.Description("up = callers, down = callees, both (default)."),
			mcp.Enum("up", "down", "both"),
		),
		mcp.WithInteger("depth",
			mcp.Description("Transitive hops (default 1, max 5)."),
			mcp.DefaultNumber(1), mcp.Min(1), mcp.Max(5),
		),
		mcp.WithString("to",
			mcp.Description("Target symbol: return the shortest call path from 'symbol' to it instead of neighbors.")),
		mcp.WithBoolean("no_source",
			mcp.Description("Omit the symbol's source body; roughly halves large responses. Pairs with direction=up.")),
		mcp.WithBoolean("no_tests",
			mcp.Description("Drop _test.go callers/callees (callers_in_tests still counts them). For production-only impact; tests are often most of a hub's rows.")),
	)
}

func graphSymbolHandler(gm *graph.Manager) func(context.Context, mcp.CallToolRequest, graphSymbolArgs) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, _ mcp.CallToolRequest, a graphSymbolArgs) (*mcp.CallToolResult, error) {
		state.RecordGraphUse("graph_symbol")
		resp, err := gm.Symbol(ctx, graph.SymbolRequest{
			Repo:      a.Repo,
			Symbol:    a.Symbol,
			Direction: a.Direction,
			Depth:     int(a.Depth),
			To:        a.To,
			NoSource:  a.NoSource,
			NoTests:   a.NoTests,
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
		mcp.WithDescription(`Public surface of one Go/Python/TS/JS package: exported symbols as one-line signatures with first doc line, no bodies. Use to orient before graph_symbol. unexported_count = internal symbols, queryable by name via graph_symbol. Degraded as in graph_symbol: freshness.stale = whole-graph fallback; stale_units = packages on carried-forward edges. Cross-check source when either fires.`),
		mcp.WithString("repo", mcp.Required(),
			mcp.Description("Absolute path to the project root.")),
		mcp.WithString("package", mcp.Required(),
			mcp.Description("Path or suffix: slash ('internal/store'), dotted (Python module), bare ('store'), or dir.")),
	)
}

func graphPackageHandler(gm *graph.Manager) func(context.Context, mcp.CallToolRequest, graphPackageArgs) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, _ mcp.CallToolRequest, a graphPackageArgs) (*mcp.CallToolResult, error) {
		state.RecordGraphUse("graph_package")
		resp, err := gm.Package(ctx, graph.PackageRequest{Repo: a.Repo, Package: a.Package})
		if err != nil {
			return graphToolErr(err), nil
		}
		return mcp.NewToolResultText(graph.RenderPackage(resp)), nil
	}
}

// ---------- graph_build_wait ----------

// Bounds for graph_build_wait, mirrored in the tool schema below.
const (
	defaultBuildWait = 10 * time.Second
	maxBuildWait     = 60 * time.Second
)

type graphBuildWaitArgs struct {
	Repo    string  `json:"repo"`
	Timeout float64 `json:"timeout,omitempty"` // seconds, default 10
}

func graphBuildWaitToolDef() mcp.Tool {
	return mcp.NewTool("graph_build_wait",
		mcp.WithDescription("Bring the repo's graph up to date, blocking until done or timeout. Attaches to an in-flight rebuild, or starts one if the graph is stale. Returns final freshness; completed: false means the timeout expired, not a failure. Use after graph_symbol returns rebuilding: true."),
		mcp.WithString("repo", mcp.Required(),
			mcp.Description("Absolute path to the project root.")),
		mcp.WithInteger("timeout",
			mcp.Description("Max seconds to wait (default 10, max 60)."),
			mcp.DefaultNumber(10), mcp.Min(1), mcp.Max(60),
		),
	)
}

func graphBuildWaitHandler(gm *graph.Manager) func(context.Context, mcp.CallToolRequest, graphBuildWaitArgs) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, _ mcp.CallToolRequest, a graphBuildWaitArgs) (*mcp.CallToolResult, error) {
		state.RecordGraphUse("graph_build_wait")
		// Clamp server-side: mcp.Min/Max above is schema advice to the client,
		// not a guarantee, so a client that ignores it must not pin a build slot
		// for an unbounded wait. Depth on graph_symbol is clamped the same way.
		timeout := time.Duration(a.Timeout) * time.Second
		if a.Timeout <= 0 {
			timeout = defaultBuildWait
		}
		timeout = min(timeout, maxBuildWait)
		resp, err := gm.WaitBuild(ctx, a.Repo, timeout)
		if err != nil {
			return graphToolErr(err), nil
		}
		// JSON for structured consumption — small payload, not a TOON table
		b, err := json.Marshal(resp)
		if err != nil {
			return mcp.NewToolResultError("marshal: " + err.Error()), nil //nolint:nilerr // MCP convention: error in result text, not Go return
		}
		return mcp.NewToolResultText(string(b)), nil
	}
}

// graphToolErr keeps not-found errors structured so the agent can distinguish
// a miss (retry with a different name) from a runtime failure.
func graphToolErr(err error) *mcp.CallToolResult {
	if errors.Is(err, graph.ErrNotFound) {
		return mcp.NewToolResultError(fmt.Sprintf(`{"status":"error","error":"not_found","message":%q,"retryable":true,"suggestion":"check spelling, or query graph_package first to list symbols"}`, err.Error()))
	}
	if errors.Is(err, graph.ErrInvalidArgument) {
		return mcp.NewToolResultError(fmt.Sprintf(`{"status":"error","error":"invalid_argument","message":%q,"retryable":false,"suggestion":"provide a valid repo and package"}`, err.Error()))
	}
	return mcp.NewToolResultError(err.Error())
}
