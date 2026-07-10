package mcpserver

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/samuelmolero26/droids-mem/internal/store"
)

// errText pulls the single text payload out of an error tool result.
func errText(t *testing.T, r *mcp.CallToolResult) string {
	t.Helper()
	if !r.IsError || len(r.Content) != 1 {
		t.Fatalf("want one error content, got IsError=%v len=%d", r.IsError, len(r.Content))
	}
	tc, ok := r.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("content not TextContent: %T", r.Content[0])
	}
	return tc.Text
}

func TestToolErr_RuntimeErrorIsStructuredAndRetryable(t *testing.T) {
	// A bare runtime error (e.g. BEGIN IMMEDIATE write-lock timeout) must not
	// leak as a raw string — it gets the structured retryable envelope.
	got := errText(t, toolErr(errors.New("begin immediate: database is locked")))
	var env struct {
		Status    string `json:"status"`
		Error     string `json:"error"`
		Retryable bool   `json:"retryable"`
	}
	if err := json.Unmarshal([]byte(got), &env); err != nil {
		t.Fatalf("runtime error payload is not JSON: %v (%q)", err, got)
	}
	if env.Status != "error" || env.Error != "internal_error" || !env.Retryable {
		t.Fatalf("unexpected envelope: %+v", env)
	}
}

func TestToolErr_ValidationErrorStillRoutes(t *testing.T) {
	// Guard the branch: a ValidationError must keep its own envelope, not the
	// generic runtime one.
	got := errText(t, toolErr(&store.ValidationError{Field: "title", Message: "required"}))
	var env struct {
		Error string `json:"error"`
		Field string `json:"field"`
	}
	if err := json.Unmarshal([]byte(got), &env); err != nil {
		t.Fatalf("validation payload is not JSON: %v", err)
	}
	if env.Error != "validation_error" || env.Field != "title" {
		t.Fatalf("validation error mis-routed: %+v", env)
	}
}
