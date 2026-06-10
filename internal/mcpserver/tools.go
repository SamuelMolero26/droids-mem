package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/oklog/ulid/v2"

	"github.com/SamuelMolero26/droids-mem/internal/store"
)

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
	Scope     string `json:"scope,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Force     bool   `json:"force,omitempty"`
}

func saveToolDef() mcp.Tool {
	return mcp.NewTool("mem_save",
		mcp.WithDescription("Persist a memory (lesson) for future agent runs. Returns the saved or matched memory id, the session_id used, and a scrub block when any sensitive content was redacted before storage."),
		mcp.WithString("kind", mcp.Required(),
			mcp.Description("Memory kind. One of: error_resolution, task_pattern, user_rule, session_summary."),
			mcp.Enum("error_resolution", "task_pattern", "user_rule", "session_summary"),
		),
		mcp.WithString("title", mcp.Required(),
			mcp.Description("Short imperative summary of the lesson (1 line). Max 200 characters.")),
		mcp.WithString("what", mcp.Required(),
			mcp.Description("What happened or what was attempted (factual context). Max 8192 characters.")),
		mcp.WithString("learned", mcp.Required(),
			mcp.Description("The reusable insight the agent should apply next time. Max 4096 characters.")),
		mcp.WithString("task_type", mcp.Required(),
			mcp.Description("Free-form workflow tag (e.g. 'crm_upload'). Scopes context retrieval and session_summary retention.")),
		mcp.WithString("tags",
			mcp.Description("Space-delimited tokens. Max 500 characters. Tags are stored unscrubbed — never embed secrets in them.")),
		mcp.WithString("scope",
			mcp.Description("Memory scope. 'shared' (default) or 'personal'. Reserved for future workspace routing."),
			mcp.Enum("personal", "shared"),
		),
		mcp.WithString("session_id",
			mcp.Description("Session id from mem_context. Omit on first save in a Run to mint a new one.")),
		mcp.WithBoolean("force",
			mcp.Description("HITL correction: overwrite an existing memory matched by fingerprint instead of skipping.")),
	)
}

func saveHandler(st *store.Store) func(context.Context, mcp.CallToolRequest, saveArgs) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, _ mcp.CallToolRequest, a saveArgs) (*mcp.CallToolResult, error) {
		resp, err := st.Save(ctx, store.SaveRequest{
			SessionID: a.SessionID,
			TaskType:  a.TaskType,
			Kind:      a.Kind,
			Title:     a.Title,
			What:      a.What,
			Learned:   a.Learned,
			Tags:      a.Tags,
			Scope:     a.Scope,
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
	return func(ctx context.Context, _ mcp.CallToolRequest, a searchArgs) (*mcp.CallToolResult, error) {
		resp, err := st.Search(ctx, store.SearchRequest{
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
	return func(ctx context.Context, _ mcp.CallToolRequest, a contextArgs) (*mcp.CallToolResult, error) {
		resp, err := st.Context(ctx, store.ContextRequest{
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
	var ve *store.ValidationError
	if errors.As(err, &ve) {
		payload := struct {
			Status          string             `json:"status"`
			Error           string             `json:"error"`
			Code            string             `json:"code,omitempty"`
			Field           string             `json:"field,omitempty"`
			Message         string             `json:"message"`
			Retryable       bool               `json:"retryable"`
			Suggestion      string             `json:"suggestion,omitempty"`
			OffendingTags   []string           `json:"offending_tags,omitempty"`
			MatchedPatterns []string           `json:"matched_patterns,omitempty"`
			Scrub           *store.ScrubReport `json:"scrub,omitempty"`
		}{
			Status:          "error",
			Error:           "validation_error",
			Code:            ve.Code,
			Field:           ve.Field,
			Message:         ve.Message,
			Retryable:       ve.Retryable,
			Suggestion:      ve.Suggestion,
			OffendingTags:   ve.OffendingTags,
			MatchedPatterns: ve.MatchedPatterns,
			Scrub:           ve.Scrub,
		}
		b, _ := json.Marshal(payload)
		return mcp.NewToolResultError(string(b))
	}
	return mcp.NewToolResultError(err.Error())
}
