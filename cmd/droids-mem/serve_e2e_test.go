package main_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	testToken      = "e2e-token"
	bootDeadline   = 5 * time.Second
	requestTimeout = 5 * time.Second
)

// pickFreePort grabs an ephemeral TCP port, then closes the listener so the
// server can bind it. Race window is unavoidable but acceptable for tests.
func pickFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

type server struct {
	t        *testing.T
	cmd      *exec.Cmd
	addr     string
	endpoint string
	token    string
	dbPath   string
	stderr   *bytes.Buffer
}

func startServer(t *testing.T) *server {
	t.Helper()
	port := pickFreePort(t)
	workDir := t.TempDir()
	dbPath := filepath.Join(workDir, "mem.db")

	s := &server{
		t:        t,
		addr:     fmt.Sprintf("127.0.0.1:%d", port),
		endpoint: "/mcp",
		token:    testToken,
		dbPath:   dbPath,
		stderr:   &bytes.Buffer{},
	}

	s.cmd = exec.Command(binaryPath, "serve")
	s.cmd.Env = append(os.Environ(),
		"DROIDS_MEM_DB="+dbPath,
		"DROIDS_MEM_HOME="+workDir,
		"DROIDS_MEM_MCP_TOKEN="+testToken,
		"DROIDS_MEM_MCP_ADDR=:"+fmt.Sprintf("%d", port),
	)
	s.cmd.Stderr = s.stderr
	if err := s.cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	deadline := time.Now().Add(bootDeadline)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + s.addr + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return s
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.stop()
	t.Fatalf("server did not become ready within %s; stderr: %s", bootDeadline, s.stderr.String())
	return nil
}

func (s *server) stop() {
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = s.cmd.Process.Kill()
		<-done
	}
}

func (s *server) url() string { return "http://" + s.addr + s.endpoint }

// jsonRPC posts a JSON-RPC request and decodes the JSON body. Streamable HTTP
// may return either application/json or text/event-stream framing — both are
// normalized here.
func (s *server) jsonRPC(t *testing.T, sessionID, token string, body map[string]any) (int, map[string]any, http.Header) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", s.url(), bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		return resp.StatusCode, nil, resp.Header
	}
	if len(respBody) == 0 {
		return resp.StatusCode, nil, resp.Header
	}

	payload := stripSSE(respBody)
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode (%s): %v\nraw: %s", resp.Header.Get("Content-Type"), err, respBody)
	}
	return resp.StatusCode, decoded, resp.Header
}

// stripSSE peels the "event: ...\ndata: <json>\n\n" framing from a Server-Sent
// Events response. Pass-through for plain JSON bodies.
func stripSSE(b []byte) []byte {
	s := string(b)
	if !strings.Contains(s, "data:") {
		return b
	}
	for line := range strings.SplitSeq(s, "\n") {
		if after, ok := strings.CutPrefix(line, "data:"); ok {
			return []byte(strings.TrimSpace(after))
		}
	}
	return b
}

// initSession performs the MCP initialize handshake and returns the session id
// the server allocated (echoed via the Mcp-Session-Id header).
func (s *server) initSession(t *testing.T) string {
	t.Helper()
	status, _, hdr := s.jsonRPC(t, "", s.token, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "e2e", "version": "1"},
		},
	})
	if status != 200 {
		t.Fatalf("initialize status=%d", status)
	}
	sid := hdr.Get("Mcp-Session-Id")
	if sid == "" {
		t.Fatalf("server did not return Mcp-Session-Id")
	}
	s.jsonRPC(t, sid, s.token, map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})
	return sid
}

// callToolRaw sends tools/call with args as raw JSON, so encodings like 5.0
// and 5e0 reach the server verbatim (json.Marshal of float64 5.0 emits 5).
// It returns the result's isError flag and first text block.
func (s *server) callToolRaw(t *testing.T, sid, name, args string) (bool, string) {
	t.Helper()
	_, resp, _ := s.jsonRPC(t, sid, s.token, map[string]any{
		"jsonrpc": "2.0",
		"id":      time.Now().UnixNano(),
		"method":  "tools/call",
		"params":  map[string]any{"name": name, "arguments": json.RawMessage(args)},
	})
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/call %s: missing result; resp=%v", name, resp)
	}
	isErr, _ := result["isError"].(bool)
	var text string
	if content, _ := result["content"].([]any); len(content) > 0 {
		first, _ := content[0].(map[string]any)
		text, _ = first["text"].(string)
	}
	return isErr, text
}

// callTool wraps tools/call and returns the inner JSON text the tool produced.
func (s *server) callTool(t *testing.T, sid, name string, args map[string]any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(args)
	_, text := s.callToolRaw(t, sid, name, string(raw))
	var parsed map[string]any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatalf("tools/call %s: inner json: %v\nraw: %s", name, err, text)
	}
	return parsed
}

// listTools returns the tools/list surface keyed by tool name.
func (s *server) listTools(t *testing.T, sid string) map[string]map[string]any {
	t.Helper()
	_, resp, _ := s.jsonRPC(t, sid, s.token, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list",
	})
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/list: missing result; resp=%v", resp)
	}
	tools := map[string]map[string]any{}
	for _, tool := range result["tools"].([]any) {
		tm := tool.(map[string]any)
		tools[tm["name"].(string)] = tm
	}
	return tools
}

// ── tests ──────────────────────────────────────────────────────────────

// TestServeE2E_Handshake covers the HTTP transport's connection lifecycle:
// bearer auth is enforced, initialize carries the proactive instructions
// (non-Claude hosts get no other nudge), and tools/list exposes exactly the
// agent surface while keeping operator commands hidden.
func TestServeE2E_Handshake(t *testing.T) {
	s := startServer(t)
	defer s.stop()

	for name, token := range map[string]string{"no token": "", "wrong token": "wrong"} {
		if status, _, _ := s.jsonRPC(t, "", token, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"}); status != http.StatusUnauthorized {
			t.Errorf("%s: status=%d, want 401", name, status)
		}
	}

	_, resp, _ := s.jsonRPC(t, "", s.token, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "e2e", "version": "1"},
		},
	})
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("initialize: missing result; resp=%v", resp)
	}
	instr, _ := result["instructions"].(string)
	for _, want := range []string{"mem_search", "mem_context", "mem_save"} {
		if !strings.Contains(instr, want) {
			t.Errorf("initialize instructions missing %q; got %q", want, instr)
		}
	}

	tools := s.listTools(t, s.initSession(t))
	for _, want := range []string{"mem_save", "mem_search", "mem_context", "mem_get", "mem_corpus", "graph_symbol", "graph_package", "graph_build_wait"} {
		if tools[want] == nil {
			t.Errorf("missing tool: %s", want)
		}
	}
	for _, hidden := range []string{"mem_list", "mem_schema", "mem_doctor"} {
		if tools[hidden] != nil {
			t.Errorf("hidden tool exposed: %s", hidden)
		}
	}
}

// TestServeE2E_SaveRoundTrip drives one save path end to end: mem_context
// mints a session that mem_save reuses, an omitted scope persists as the
// 'personal' default the schema advertises (#210 — agents trust the
// description), and an identical re-save is skipped as a duplicate.
func TestServeE2E_SaveRoundTrip(t *testing.T) {
	s := startServer(t)
	defer s.stop()
	sid := s.initSession(t)

	props := s.listTools(t, sid)["mem_save"]["inputSchema"].(map[string]any)["properties"].(map[string]any)
	if desc, _ := props["scope"].(map[string]any)["description"].(string); !strings.Contains(desc, "'personal' (default)") {
		t.Fatalf("mem_save scope description must name 'personal' as default; got %q", desc)
	}

	ctxResp := s.callTool(t, sid, "mem_context", map[string]any{"task_type": "smoke"})
	minted, _ := ctxResp["session_id"].(string)
	if !strings.HasPrefix(minted, "sess_") {
		t.Fatalf("expected session_id with sess_ prefix, got %q", minted)
	}
	if _, ok := ctxResp["context"].(map[string]any); !ok {
		t.Fatalf("mem_context envelope missing 'context' field; got %v", ctxResp)
	}

	args := map[string]any{
		"kind":       "task_pattern",
		"title":      "smoke save",
		"what":       "saved without a scope argument",
		"learned":    "omitted scope must persist as personal",
		"task_type":  "smoke",
		"session_id": minted,
	}
	first := s.callTool(t, sid, "mem_save", args)
	if first["status"] != "saved" {
		t.Fatalf("first save status=%v, want saved; resp=%v", first["status"], first)
	}
	if first["session_id"] != minted {
		t.Fatalf("save returned session_id=%v, want %s", first["session_id"], minted)
	}
	if id, _ := first["id"].(string); !strings.HasPrefix(id, "mem_") {
		t.Fatalf("expected memory id with mem_ prefix, got %q", id)
	}
	if got := s.callTool(t, sid, "mem_get", map[string]any{"id": first["id"]}); got["scope"] != "personal" {
		t.Fatalf("omitted scope persisted as %v, want personal", got["scope"])
	}

	second := s.callTool(t, sid, "mem_save", args)
	if second["status"] != "skipped" || second["reason"] != "duplicate" {
		t.Fatalf("re-save status=%v reason=%v, want skipped/duplicate", second["status"], second["reason"])
	}
	if second["matched_id"] != first["id"] {
		t.Fatalf("matched_id=%v, want %v", second["matched_id"], first["id"])
	}
}

func TestServeE2E_GracefulShutdownExitsZero(t *testing.T) {
	s := startServer(t)
	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) && ee.ExitCode() != 0 {
				t.Fatalf("exit code=%d, stderr=%s", ee.ExitCode(), s.stderr.String())
			}
		}
	case <-time.After(5 * time.Second):
		_ = s.cmd.Process.Kill()
		<-done
		t.Fatalf("server did not exit within grace period; stderr=%s", s.stderr.String())
	}
	if !strings.Contains(s.stderr.String(), "shutdown complete") {
		t.Errorf("missing 'shutdown complete' log; stderr=%s", s.stderr.String())
	}
}
