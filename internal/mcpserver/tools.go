package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/oklog/ulid/v2"
	"github.com/samuelmolero26/droids-mem/internal/scrub"

	"github.com/samuelmolero26/droids-mem/internal/store"
)

func registerTools(s *server.MCPServer, st *store.Store) {
	s.AddTool(saveToolDef(), mcp.NewTypedToolHandler(saveHandler(st)))
	s.AddTool(searchToolDef(), mcp.NewTypedToolHandler(searchHandler(st)))
	s.AddTool(contextToolDef(), mcp.NewTypedToolHandler(contextHandler(st)))
	s.AddTool(getToolDef(), mcp.NewTypedToolHandler(getHandler(st)))
	s.AddTool(corpusToolDef(), mcp.NewTypedToolHandler(corpusHandler(st)))
}

// ---------- mem_save ----------

type saveArgs struct {
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	What       string `json:"what"`
	Learned    string `json:"learned"`
	TaskType   string `json:"task_type"`
	Tags       string `json:"tags,omitempty"`
	Scope      string `json:"scope,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	Force      bool   `json:"force,omitempty"`
	Supersedes string `json:"supersedes,omitempty"`
	DryRun     bool   `json:"dry_run,omitempty"`
}

func saveToolDef() mcp.Tool {
	return mcp.NewTool("mem_save",
		mcp.WithDescription("Save a reusable lesson, unprompted: a fix that worked, a repeatable approach, a user correction. Returns the saved or matched id, the session_id, and a scrub block if sensitive content was redacted."),
		mcp.WithString("kind", mcp.Required(),
			mcp.Description("Memory kind."),
			mcp.Enum("error_resolution", "task_pattern", "user_rule", "session_summary"),
		),
		mcp.WithString("title", mcp.Required(),
			mcp.Description("One-line imperative summary. Max 200 bytes.")),
		mcp.WithString("what", mcp.Required(),
			mcp.Description("What happened or was attempted. Max 8192 bytes.")),
		mcp.WithString("learned", mcp.Required(),
			mcp.Description("The insight to apply next time. Max 4096 bytes.")),
		mcp.WithString("task_type", mcp.Required(),
			mcp.Description("Workflow tag, e.g. the repo name. Scopes context retrieval and session_summary retention.")),
		mcp.WithString("tags",
			mcp.Description("Space-delimited tokens. Max 500 bytes. Stored unscrubbed: no secrets.")),
		mcp.WithString("scope",
			mcp.Description("'personal' (default) or 'shared'. Only 'shared' enters the share pool."),
			mcp.Enum("personal", "shared"),
		),
		mcp.WithString("session_id",
			mcp.Description("Session id from mem_context. Omit on a Run's first save to mint one.")),
		mcp.WithBoolean("force",
			mcp.Description("Human correction: overwrite the fingerprint-matched memory instead of skipping.")),
		mcp.WithString("supersedes",
			mcp.Description("Id of a memory this save replaces; hard-deleted on successful insert (needs same kind/task_type/scope, else no-op). Response echoes 'superseded' with that id.")),
		mcp.WithBoolean("dry_run",
			mcp.Description("Preview without persisting: full validate → scrub → dedupe under the real write lock, then rollback. Returns {status:\"dry_run\", would, matched_id, reason}.")),
	)
}

func saveHandler(st *store.Store) func(context.Context, mcp.CallToolRequest, saveArgs) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, _ mcp.CallToolRequest, a saveArgs) (*mcp.CallToolResult, error) {
		resp, err := st.Save(ctx, store.SaveRequest{
			SessionID:  a.SessionID,
			TaskType:   a.TaskType,
			Kind:       a.Kind,
			Title:      a.Title,
			What:       a.What,
			Learned:    a.Learned,
			Tags:       a.Tags,
			Scope:      a.Scope,
			Force:      a.Force,
			Supersedes: a.Supersedes,
			DryRun:     a.DryRun,
		})
		if err != nil {
			return toolErr(err), nil
		}
		if a.DryRun {
			// Store rolled the txn back — resp.ID is a phantom. Mirror the CLI's
			// dry_run envelope so the agent never mistakes a preview for a write.
			return toolJSON(map[string]any{
				"status":     "dry_run",
				"would":      resp.Status,
				"matched_id": resp.MatchedID,
				"reason":     resp.Reason,
				"session_id": resp.SessionID,
			})
		}
		return toolJSON(resp)
	}
}

// ---------- mem_search ----------

type searchArgs struct {
	Query       string  `json:"query"`
	TaskType    string  `json:"task_type,omitempty"`
	Kind        string  `json:"kind,omitempty"`
	Limit       float64 `json:"limit,omitempty"`
	AllProjects bool    `json:"all_projects,omitempty"`
}

func searchToolDef() mcp.Tool {
	return mcp.NewTool("mem_search",
		mcp.WithDescription(`BM25 full-text search over memories, re-ranked by token overlap. Use mid-run on a topic shift or a gap mem_context missed. For code structure use graph_symbol/graph_package.

Results are ranked previews: learned_preview is the first 500 chars (with a total-length marker if truncated); mem_get an id for the full body. authored_at (unix seconds) is when the lesson was written (imports predate created_at); verify old lessons against current code. On total == 0, a display-only message names the empty state; gate on total, not message.`),
		mcp.WithString("query", mcp.Required(),
			mcp.Description("Search phrase.")),
		mcp.WithString("task_type",
			mcp.Description("Filter to one project. Ignored when all_projects=true.")),
		mcp.WithString("kind",
			mcp.Description("Kind filter."),
			mcp.Enum("error_resolution", "task_pattern", "user_rule", "session_summary"),
		),
		mcp.WithInteger("limit",
			mcp.Description("Max results (default 5, max 20)."),
			mcp.DefaultNumber(5), mcp.Min(1), mcp.Max(20),
		),
		mcp.WithBoolean("all_projects",
			mcp.Description("Search every task_type (default false): for problems spanning repos, or when the owning project is unknown.")),
	)
}

func searchHandler(st *store.Store) func(context.Context, mcp.CallToolRequest, searchArgs) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, _ mcp.CallToolRequest, a searchArgs) (*mcp.CallToolResult, error) {
		resp, err := st.Search(ctx, store.SearchRequest{
			Query:       a.Query,
			TaskType:    a.TaskType,
			Kind:        a.Kind,
			Limit:       int(a.Limit),
			AllProjects: a.AllProjects,
		})
		if err != nil {
			return toolErr(err), nil
		}
		return toolJSON(store.ToCompactSearchResponse(resp, "Call mem_get with a result id to read the full body"))
	}
}

// ---------- mem_context ----------

type contextArgs struct {
	TaskType  string `json:"task_type"`
	Query     string `json:"query,omitempty"`
	Mode      string `json:"mode,omitempty"`
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
		mcp.WithDescription("Load the orientation bundle for a task_type. Call first at every task start: task_type = the git repo name (same string every session), query = a short task description. Returns always-tier memories (full body), browse-tier titles/snippets, and a session_id for later mem_save calls."),
		mcp.WithString("task_type", mcp.Required(),
			mcp.Description("Workflow tag scoping the bundle.")),
		mcp.WithString("query",
			mcp.Description("Short task description; ranks the browse tier by relevance. Absent or punctuation-only ranks by recency, missing older lessons. Invalid with mode=refresh.")),
		mcp.WithString("mode",
			mcp.Description("orient (default: always tier + browse snippets), deep (all rules and browse bodies in full), refresh (always tier only; cheap mid-run re-anchor).")),
		mcp.WithString("session_id",
			mcp.Description("Omit only on a Run's first call (mints one). On later calls (topic pivot, mode=refresh) pass it back, or the Run's memories split across two ids.")),
	)
}

func contextHandler(st *store.Store) func(context.Context, mcp.CallToolRequest, contextArgs) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, _ mcp.CallToolRequest, a contextArgs) (*mcp.CallToolResult, error) {
		resp, err := st.Context(ctx, store.ContextRequest{
			TaskType: a.TaskType,
			Query:    a.Query,
			Mode:     store.ContextMode(a.Mode),
		})
		if err != nil {
			return toolErr(err), nil
		}
		sid := a.SessionID
		if sid == "" {
			sid = "sess_" + ulid.Make().String()
		}
		if len(resp.Browse) > 0 {
			// Contextual disclosure (AXI §9): stub IDs are expandable via
			// mem_get. Omitted when Browse is empty (omit-when-self-contained).
			resp.Help = []string{"Call mem_get with a browse-tier id to read the full body"}
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
		mcp.WithDescription("Fetch one memory's full body by id (a mem_context browse id or mem_search result id). Expand a stub before relying on it; list responses carry only previews."),
		mcp.WithString("id", mcp.Required(),
			mcp.Description("Memory id, e.g. 'mem_01J...'.")),
	)
}

func getHandler(st *store.Store) func(context.Context, mcp.CallToolRequest, getArgs) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, _ mcp.CallToolRequest, a getArgs) (*mcp.CallToolResult, error) {
		m, err := st.Get(ctx, a.ID)
		if err != nil {
			return toolErr(err), nil
		}
		if m == nil {
			return mcp.NewToolResultError(fmt.Sprintf("memory %q not found", a.ID)), nil
		}
		return toolJSON(m)
	}
}

// ---------- mem_corpus ----------

type corpusArgs struct {
	Limit float64 `json:"limit,omitempty"`
}

// RecentSessionStub is a lightweight summary of one session_summary memory
// for the mem_corpus response. Origin names how the row was authored
// ("manual" for explicit saves, "auto" for the session-end path) so the
// agent can tell its own recaps apart from automatic ones.
type RecentSessionStub struct {
	Title     string `json:"title"`
	CreatedAt int64  `json:"created_at"`
	Origin    string `json:"origin,omitempty"`
}

type corpusResponse struct {
	TaskTypes      []store.TaskTypeCount `json:"task_types"`
	ByKind         map[string]int        `json:"by_kind"`
	Total          int                   `json:"total"`
	RecentSessions []RecentSessionStub   `json:"recent_sessions,omitempty"`
}

func corpusToolDef() mcp.Tool {
	return mcp.NewTool("mem_corpus",
		mcp.WithDescription("Corpus census: task_types with counts, kind breakdown, total, and recent session_summary titles (newest first, each with origin manual/auto). Call only when unsure which task_type this project uses, or to check corpus health."),
		mcp.WithInteger("limit",
			mcp.Description("Max recent_sessions (default 5, max 20)."),
			mcp.DefaultNumber(5), mcp.Min(1), mcp.Max(20),
		),
	)
}

func corpusHandler(st *store.Store) func(context.Context, mcp.CallToolRequest, corpusArgs) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, _ mcp.CallToolRequest, a corpusArgs) (*mcp.CallToolResult, error) {
		limit := int(a.Limit)
		if limit <= 0 {
			limit = 5
		}
		if limit > 20 {
			limit = 20
		}

		// Gather corpus data from multiple store methods.
		counts, err := st.Counts(ctx)
		if err != nil {
			return toolErr(err), nil
		}

		types, err := st.ListTaskTypes(ctx)
		if err != nil {
			return toolErr(err), nil
		}

		sessions, err := st.RecentSummaries(ctx, store.RecentSessionsRequest{Limit: limit})
		if err != nil {
			return toolErr(err), nil
		}

		stubs := make([]RecentSessionStub, 0, len(sessions.Sessions))
		for _, s := range sessions.Sessions {
			stubs = append(stubs, RecentSessionStub{Title: s.Title, CreatedAt: s.CreatedAt, Origin: s.Origin})
		}

		resp := corpusResponse{
			TaskTypes:      types,
			ByKind:         counts.ByKind,
			Total:          counts.Total,
			RecentSessions: stubs,
		}
		return toolJSON(resp)
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
// ValidationError fields are marshaled into a single JSON envelope so the
// agent sees code/field/message/retryable/suggestion + optional metadata
// (offending_tags, matched_patterns, scrub) without parsing prose.
func toolErr(err error) *mcp.CallToolResult {
	if ve, ok := errors.AsType[*store.ValidationError](err); ok {
		payload := struct {
			Status          string             `json:"status"`
			Error           string             `json:"error"`
			Code            string             `json:"code,omitempty"`
			Field           string             `json:"field,omitempty"`
			Message         string             `json:"message"`
			Retryable       bool               `json:"retryable"`
			Suggestion      string             `json:"suggestion,omitempty"`
			Limit           int                `json:"limit,omitempty"`
			Actual          int                `json:"actual,omitempty"`
			OffendingTags   []string           `json:"offending_tags,omitempty"`
			MatchedPatterns []string           `json:"matched_patterns,omitempty"`
			Scrub           *scrub.ScrubReport `json:"scrub,omitempty"`
		}{
			Status:          "error",
			Error:           "validation_error",
			Code:            ve.Code,
			Field:           ve.Field,
			Message:         ve.Message,
			Retryable:       ve.Retryable,
			Suggestion:      ve.Suggestion,
			Limit:           ve.Limit,
			Actual:          ve.Actual,
			OffendingTags:   ve.OffendingTags,
			MatchedPatterns: ve.MatchedPatterns,
			Scrub:           ve.Scrub,
		}
		b, merr := json.Marshal(payload)
		if merr != nil {
			// Scrub carries float rates; an unencodable one must not cost the
			// agent the reason its save was rejected.
			return mcp.NewToolResultError(ve.Message)
		}
		return mcp.NewToolResultError(string(b))
	}
	// Runtime errors (dominant case: BEGIN IMMEDIATE write-lock timeout under
	// concurrent writers, ADR-0024) get the same structured envelope so the
	// agent sees retryable/suggestion instead of a raw SQLITE_BUSY string.
	// ponytail: retryable:true for all runtime errors here — the common one is
	// transient (busy); the rare non-retryable ones (marshal/disk) self-limit
	// on one retry. Sniff the sqlite code only if that becomes a real problem.
	b, merr := json.Marshal(struct {
		Status     string `json:"status"`
		Error      string `json:"error"`
		Message    string `json:"message"`
		Retryable  bool   `json:"retryable"`
		Suggestion string `json:"suggestion"`
	}{
		Status:     "error",
		Error:      "internal_error",
		Message:    err.Error(),
		Retryable:  true,
		Suggestion: "retry after a short backoff",
	})
	if merr != nil {
		return mcp.NewToolResultError(err.Error())
	}
	return mcp.NewToolResultError(string(b))
}
