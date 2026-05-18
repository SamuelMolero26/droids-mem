// droids-mem-mcp is an MCP (Model Context Protocol) bridge for droids-mem.
//
// Architecture and rationale: see docs/adr/0003-mcp-bridge-for-agentspan.md.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/oklog/ulid/v2"

	"github.com/samuelmolero/droids-mem/internal/db"
	"github.com/samuelmolero/droids-mem/internal/store"
)

const shutdownGrace = 10 * time.Second

const (
	defaultAddr     = ":7777"
	defaultEndpoint = "/mcp"
	serverName      = "droids-mem-mcp"
	serverVersion   = "0.1.0"
)

func main() {
	addr := envOr("DROIDS_MEM_MCP_ADDR", defaultAddr)
	endpoint := envOr("DROIDS_MEM_MCP_ENDPOINT", defaultEndpoint)
	token := os.Getenv("DROIDS_MEM_MCP_TOKEN")
	if token == "" {
		log.Fatal("DROIDS_MEM_MCP_TOKEN is required (bearer auth)")
	}

	database, err := db.Open()
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer database.Close()

	st := store.New(database)

	s := server.NewMCPServer(serverName, serverVersion,
		server.WithToolCapabilities(true),
		server.WithLogging(),
	)
	registerTools(s, st)

	mcpHandler := server.NewStreamableHTTPServer(s,
		server.WithEndpointPath(endpoint),
		server.WithHeartbeatInterval(30*time.Second),
	)

	mux := http.NewServeMux()
	mux.Handle(endpoint, mcpHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	wrapped := bearerAuth(token, endpoint, mux)

	log.Printf("%s %s listening on %s (endpoint=%s)", serverName, serverVersion, addr, endpoint)
	srv := &http.Server{
		Addr:              addr,
		Handler:           wrapped,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Graceful shutdown: SIGINT/SIGTERM triggers Shutdown(), which lets
	// in-flight MCP calls complete within shutdownGrace before the process
	// exits. The DB Close in main's deferred chain runs after Shutdown
	// returns, so no writer txn is killed mid-flight.
	stopCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
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
			log.Fatalf("serve: %v", err)
		}
	case <-stopCtx.Done():
		log.Printf("shutdown signal received; draining (grace=%s)", shutdownGrace)
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			log.Printf("graceful shutdown failed: %v (forcing close)", err)
			_ = srv.Close()
		}
		log.Printf("shutdown complete")
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

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ---------- tool registration ----------

func registerTools(s *server.MCPServer, st *store.Store) {
	s.AddTool(saveToolDef(), mcp.NewTypedToolHandler(saveHandler(st)))
	s.AddTool(searchToolDef(), mcp.NewTypedToolHandler(searchHandler(st)))
	s.AddTool(contextToolDef(), mcp.NewTypedToolHandler(contextHandler(st)))
	s.AddTool(getToolDef(), mcp.NewTypedToolHandler(getHandler(st)))
}

// ---------- mem_save ----------

type saveArgs struct {
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	What      string `json:"what"`
	Learned   string `json:"learned"`
	TaskType  string `json:"task_type"`
	Tags      string `json:"tags,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Force     bool   `json:"force,omitempty"`
}

func saveToolDef() mcp.Tool {
	return mcp.NewTool("mem_save",
		mcp.WithDescription("Persist a memory (lesson) for future agent runs. Returns the saved or matched memory id and the session_id used."),
		mcp.WithString("kind", mcp.Required(),
			mcp.Description("Memory kind. One of: error_resolution, task_pattern, user_rule, session_summary."),
			mcp.Enum("error_resolution", "task_pattern", "user_rule", "session_summary"),
		),
		mcp.WithString("title", mcp.Required(),
			mcp.Description("Short imperative summary of the lesson (1 line).")),
		mcp.WithString("what", mcp.Required(),
			mcp.Description("What happened or what was attempted (factual context).")),
		mcp.WithString("learned", mcp.Required(),
			mcp.Description("The reusable insight the agent should apply next time.")),
		mcp.WithString("task_type", mcp.Required(),
			mcp.Description("Free-form workflow tag (e.g. 'crm_upload'). Scopes context retrieval and session_summary retention.")),
		mcp.WithString("tags",
			mcp.Description("Space-delimited tokens.")),
		mcp.WithString("session_id",
			mcp.Description("Session id from mem_context. Omit on first save in a Run to mint a new one.")),
		mcp.WithBoolean("force",
			mcp.Description("HITL correction: overwrite an existing memory matched by fingerprint instead of skipping.")),
	)
}

func saveHandler(st *store.Store) func(context.Context, mcp.CallToolRequest, saveArgs) (*mcp.CallToolResult, error) {
	return func(_ context.Context, _ mcp.CallToolRequest, a saveArgs) (*mcp.CallToolResult, error) {
		resp, err := st.Save(store.SaveRequest{
			SessionID: a.SessionID,
			TaskType:  a.TaskType,
			Kind:      a.Kind,
			Title:     a.Title,
			What:      a.What,
			Learned:   a.Learned,
			Tags:      a.Tags,
			Force:     a.Force,
		})
		if err != nil {
			return toolErr(err), nil
		}
		return toolJSON(resp)
	}
}

// ---------- mem_search ----------

type searchArgs struct {
	Query    string `json:"query"`
	TaskType string `json:"task_type,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

func searchToolDef() mcp.Tool {
	return mcp.NewTool("mem_search",
		mcp.WithDescription("Full-text search across stored memories ranked by BM25."),
		mcp.WithString("query", mcp.Required(),
			mcp.Description("Free-text search phrase.")),
		mcp.WithString("task_type",
			mcp.Description("Optional task_type filter.")),
		mcp.WithString("kind",
			mcp.Description("Optional kind filter."),
			mcp.Enum("error_resolution", "task_pattern", "user_rule", "session_summary"),
		),
		mcp.WithNumber("limit",
			mcp.Description("Max results (default 5, max 20)."),
			mcp.DefaultNumber(5), mcp.Min(1), mcp.Max(20),
		),
	)
}

func searchHandler(st *store.Store) func(context.Context, mcp.CallToolRequest, searchArgs) (*mcp.CallToolResult, error) {
	return func(_ context.Context, _ mcp.CallToolRequest, a searchArgs) (*mcp.CallToolResult, error) {
		resp, err := st.Search(store.SearchRequest{
			Query:    a.Query,
			TaskType: a.TaskType,
			Kind:     a.Kind,
			Limit:    a.Limit,
		})
		if err != nil {
			return toolErr(err), nil
		}
		return toolJSON(resp)
	}
}

// ---------- mem_context ----------

type contextArgs struct {
	TaskType  string `json:"task_type"`
	Query     string `json:"query,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// contextEnvelope augments store.ContextResponse with the session_id the agent
// should thread through subsequent mem_save calls in this Run. See ADR 0003 —
// hybrid agent-owned session ownership.
type contextEnvelope struct {
	SessionID string                 `json:"session_id"`
	Context   *store.ContextResponse `json:"context"`
}

func contextToolDef() mcp.Tool {
	return mcp.NewTool("mem_context",
		mcp.WithDescription("Load the two-tier orientation bundle for a task_type at the start of a Run. Returns always-tier memories (full body) + browse-tier titles/snippets, plus a session_id to thread through subsequent mem_save calls."),
		mcp.WithString("task_type", mcp.Required(),
			mcp.Description("Workflow tag scoping the bundle.")),
		mcp.WithString("query",
			mcp.Description("Optional focus query for browse-tier ranking. Falls back to task_type tokens.")),
		mcp.WithString("session_id",
			mcp.Description("Optional pre-existing session_id to reuse. Omit to mint a fresh one for this Run.")),
	)
}

func contextHandler(st *store.Store) func(context.Context, mcp.CallToolRequest, contextArgs) (*mcp.CallToolResult, error) {
	return func(_ context.Context, _ mcp.CallToolRequest, a contextArgs) (*mcp.CallToolResult, error) {
		resp, err := st.Context(store.ContextRequest{
			TaskType: a.TaskType,
			Query:    a.Query,
		})
		if err != nil {
			return toolErr(err), nil
		}
		sid := a.SessionID
		if sid == "" {
			sid = "sess_" + ulid.Make().String()
		}
		return toolJSON(contextEnvelope{SessionID: sid, Context: resp})
	}
}

// ---------- mem_get ----------

type getArgs struct {
	ID string `json:"id"`
}

func getToolDef() mcp.Tool {
	return mcp.NewTool("mem_get",
		mcp.WithDescription("Fetch the full body of a single memory by id (typically a browse-tier id returned by mem_context or mem_search)."),
		mcp.WithString("id", mcp.Required(),
			mcp.Description("Memory id, e.g. 'mem_01J...'.")),
	)
}

func getHandler(st *store.Store) func(context.Context, mcp.CallToolRequest, getArgs) (*mcp.CallToolResult, error) {
	return func(_ context.Context, _ mcp.CallToolRequest, a getArgs) (*mcp.CallToolResult, error) {
		m, err := st.Get(a.ID)
		if err != nil {
			return toolErr(err), nil
		}
		if m == nil {
			return mcp.NewToolResultError(fmt.Sprintf("memory %q not found", a.ID)), nil
		}
		return toolJSON(m)
	}
}

// ---------- helpers ----------

func toolJSON(v any) (*mcp.CallToolResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal response: %w", err)
	}
	return mcp.NewToolResultText(string(b)), nil
}

// toolErr wraps store errors (validation + runtime) as MCP tool errors so the
// agent receives a structured failure instead of a transport-level exception.
func toolErr(err error) *mcp.CallToolResult {
	var ve *store.ValidationError
	if errors.As(err, &ve) {
		payload := map[string]string{
			"error":   "validation_error",
			"field":   ve.Field,
			"message": ve.Message,
		}
		b, _ := json.Marshal(payload)
		return mcp.NewToolResultError(string(b))
	}
	return mcp.NewToolResultError(err.Error())
}
