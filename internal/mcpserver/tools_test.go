package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/samuelmolero26/droids-mem/internal/db"
	"github.com/samuelmolero26/droids-mem/internal/store"
	_ "modernc.org/sqlite"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Init(conn); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return store.New(conn)
}

// okText pulls the single text payload out of a success tool result.
func okText(t *testing.T, r *mcp.CallToolResult) string {
	t.Helper()
	if r.IsError || len(r.Content) != 1 {
		t.Fatalf("want one success content, got IsError=%v len=%d", r.IsError, len(r.Content))
	}
	tc, ok := r.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("content not TextContent: %T", r.Content[0])
	}
	return tc.Text
}

func saveArgsFixture() saveArgs {
	return saveArgs{
		Kind:     "task_pattern",
		Title:    "Cache API responses with lru",
		What:     "wrapped the fetch function",
		Learned:  "@lru_cache on the fetcher, size 1000",
		TaskType: "handlertest",
	}
}

func TestSaveHandler_HappyPath(t *testing.T) {
	st := newTestStore(t)
	res, err := saveHandler(st)(context.Background(), mcp.CallToolRequest{}, saveArgsFixture())
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	var resp store.SaveResponse
	if err := json.Unmarshal([]byte(okText(t, res)), &resp); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if resp.Status != "saved" || resp.ID == "" {
		t.Fatalf("want saved with id, got %+v", resp)
	}
}

// mem_save must offer dry_run parity with the CLI: preview the outcome without
// persisting anything (AXI — MCP surface should not be strictly weaker).
func TestSaveHandler_DryRunDoesNotPersist(t *testing.T) {
	st := newTestStore(t)
	a := saveArgsFixture()
	a.DryRun = true

	res, err := saveHandler(st)(context.Background(), mcp.CallToolRequest{}, a)
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	var env struct {
		Status string `json:"status"`
		Would  string `json:"would"`
	}
	if err := json.Unmarshal([]byte(okText(t, res)), &env); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if env.Status != "dry_run" || env.Would != "saved" {
		t.Fatalf("want status=dry_run would=saved, got %+v", env)
	}

	// Nothing persisted: a real search finds no rows.
	sr, err := st.Search(context.Background(), store.SearchRequest{Query: "lru cache", TaskType: "handlertest"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if sr.Total != 0 {
		t.Fatalf("dry-run persisted %d rows, want 0", sr.Total)
	}
}

func TestSaveHandler_ValidationErrorRoutes(t *testing.T) {
	st := newTestStore(t)
	a := saveArgsFixture()
	a.Kind = "bogus_kind"
	res, err := saveHandler(st)(context.Background(), mcp.CallToolRequest{}, a)
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	var env struct {
		Error string `json:"error"`
		Field string `json:"field"`
	}
	if err := json.Unmarshal([]byte(errText(t, res)), &env); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if env.Error != "validation_error" || env.Field != "kind" {
		t.Fatalf("mis-routed validation: %+v", env)
	}
}

func TestSearchHandler_ReturnsTotal(t *testing.T) {
	st := newTestStore(t)
	if _, err := saveHandler(st)(context.Background(), mcp.CallToolRequest{}, saveArgsFixture()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := searchHandler(st)(context.Background(), mcp.CallToolRequest{}, searchArgs{Query: "lru cache", TaskType: "handlertest"})
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	var resp store.SearchResponse
	if err := json.Unmarshal([]byte(okText(t, res)), &resp); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("want total 1, got %d", resp.Total)
	}
}

func TestContextHandler_MintsSessionID(t *testing.T) {
	st := newTestStore(t)
	res, err := contextHandler(st)(context.Background(), mcp.CallToolRequest{}, contextArgs{TaskType: "handlertest"})
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	var env contextEnvelope
	if err := json.Unmarshal([]byte(okText(t, res)), &env); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if env.SessionID == "" || env.Context == nil {
		t.Fatalf("want session_id + context, got %+v", env)
	}
}

func TestGetHandler_NotFound(t *testing.T) {
	st := newTestStore(t)
	res, err := getHandler(st)(context.Background(), mcp.CallToolRequest{}, getArgs{ID: "mem_DOESNOTEXIST"})
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	if !res.IsError {
		t.Fatalf("want error result for missing id")
	}
}

func TestCorpusHandler_Census(t *testing.T) {
	st := newTestStore(t)
	if _, err := saveHandler(st)(context.Background(), mcp.CallToolRequest{}, saveArgsFixture()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := corpusHandler(st)(context.Background(), mcp.CallToolRequest{}, corpusArgs{})
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	var resp corpusResponse
	if err := json.Unmarshal([]byte(okText(t, res)), &resp); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if resp.Total != 1 || len(resp.TaskTypes) != 1 {
		t.Fatalf("want 1 memory in 1 task_type, got total=%d types=%d", resp.Total, len(resp.TaskTypes))
	}
}
