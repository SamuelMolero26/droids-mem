package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
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
	var resp store.SearchCompactResponse
	if err := json.Unmarshal([]byte(okText(t, res)), &resp); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("want total 1, got %d", resp.Total)
	}
}

// Browse-tier stubs disclose mem_get; an empty browse stays self-contained.
func TestContextHandler_BrowseDisclosesGet(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.Save(context.Background(), store.SaveRequest{
		TaskType: "helpctx", Kind: "error_resolution",
		Title: "Browse stub", What: "visible snippet here", Learned: "the lesson", Tags: "stub",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	res, err := contextHandler(st)(context.Background(), mcp.CallToolRequest{}, contextArgs{TaskType: "helpctx"})
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	var env contextEnvelope
	if err := json.Unmarshal([]byte(okText(t, res)), &env); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if len(env.Context.Browse) == 0 {
		t.Fatal("want browse-tier stub, got none")
	}
	if len(env.Context.Help) != 1 || !strings.Contains(env.Context.Help[0], "mem_get") {
		t.Errorf("browse help = %v, want mem_get disclosure", env.Context.Help)
	}

	empty, err := contextHandler(st)(context.Background(), mcp.CallToolRequest{}, contextArgs{TaskType: "helpctx-empty"})
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	var emptyEnv contextEnvelope
	if err := json.Unmarshal([]byte(okText(t, empty)), &emptyEnv); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if len(emptyEnv.Context.Help) != 0 {
		t.Errorf("empty browse help = %v, want none", emptyEnv.Context.Help)
	}
}

// Successful get is self-contained: full body, no help noise (AXI §9
// omit-when-self-contained).
func TestGetHandler_HappyPathHasNoHelp(t *testing.T) {
	st := newTestStore(t)
	saved, err := st.Save(context.Background(), store.SaveRequest{
		TaskType: "getnohelp", Kind: "task_pattern",
		Title: "Full body", What: "context", Learned: "the full lesson", Tags: "full",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := getHandler(st)(context.Background(), mcp.CallToolRequest{}, getArgs{ID: saved.ID})
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(okText(t, res)), &decoded); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if decoded["learned"] != "the full lesson" {
		t.Errorf("get lost full learned: %v", decoded)
	}
	if _, ok := decoded["help"]; ok {
		t.Errorf("detail view carries help noise: %v", decoded)
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

// Corpus recent_sessions is the agent-facing recency timeline: manual
// summaries (the only kind MCP mem_save produces) must appear alongside
// autos, newest first, each labeled with its origin. A non-summary memory
// must never appear even when it is the newest row.
func TestCorpusHandler_IncludesManualSummariesNewerThanAutos(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	auto, err := st.Save(ctx, store.SaveRequest{
		TaskType: "claude_session", Kind: "session_summary",
		Title: "Auto nightly session recap", What: "flushed session context",
		Learned: "checkpoint pipeline state for the next run alpha",
		Origin:  "auto",
	})
	if err != nil {
		t.Fatalf("seed auto: %v", err)
	}
	manual, err := st.Save(ctx, store.SaveRequest{
		TaskType: "crm_upload", Kind: "session_summary",
		Title:   "Manual upload retry recap", What: "investigated the gateway timeout",
		Learned: "cap CRM batch uploads at 200 rows to dodge the gateway timeout beta",
	})
	if err != nil {
		t.Fatalf("seed manual: %v", err)
	}
	noise, err := st.Save(ctx, store.SaveRequest{
		TaskType: "crm_upload", Kind: "task_pattern",
		Title:   "Newest row but not a summary", What: "a reusable fix",
		Learned: "pattern lesson unrelated to session recaps gamma",
	})
	if err != nil {
		t.Fatalf("seed noise: %v", err)
	}

	// Pin created_at deterministically: Save stamps time.Now(), so same-second
	// rows would tie. Manual is newest, auto older, noise newest overall (must
	// still be excluded by the kind filter).
	pin := func(id string, ts int64) {
		t.Helper()
		if _, err := st.DB().Exec(
			`UPDATE memories SET created_at = ?, updated_at = ? WHERE id = ?`, ts, ts, id,
		); err != nil {
			t.Fatalf("pin %s: %v", id, err)
		}
	}
	pin(auto.ID, 100)
	pin(manual.ID, 300)
	pin(noise.ID, 400)

	res, err := corpusHandler(st)(ctx, mcp.CallToolRequest{}, corpusArgs{})
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	var resp corpusResponse
	if err := json.Unmarshal([]byte(okText(t, res)), &resp); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if len(resp.RecentSessions) != 2 {
		t.Fatalf("want 2 session summaries, got %d: %+v", len(resp.RecentSessions), resp.RecentSessions)
	}
	first, second := resp.RecentSessions[0], resp.RecentSessions[1]
	if first.Title != "Manual upload retry recap" || first.Origin != "manual" {
		t.Errorf("recent[0] = %+v, want the newer manual summary with origin=manual", first)
	}
	if second.Title != "Auto nightly session recap" || second.Origin != "auto" {
		t.Errorf("recent[1] = %+v, want the older auto summary with origin=auto", second)
	}
	if first.CreatedAt <= second.CreatedAt {
		t.Errorf("recent_sessions not newest-first: %d then %d", first.CreatedAt, second.CreatedAt)
	}
}
