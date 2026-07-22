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
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "data:") {
			return []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
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

// callTool wraps tools/call and returns the inner JSON text the tool produced.
func (s *server) callTool(t *testing.T, sid, name string, args map[string]any) map[string]any {
	t.Helper()
	_, resp, _ := s.jsonRPC(t, sid, s.token, map[string]any{
		"jsonrpc": "2.0",
		"id":      time.Now().UnixNano(),
		"method":  "tools/call",
		"params":  map[string]any{"name": name, "arguments": args},
	})
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/call %s: missing result; resp=%v", name, resp)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("tools/call %s: empty content; resp=%v", name, resp)
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatalf("tools/call %s: inner json: %v\nraw: %s", name, err, text)
	}
	return parsed
}

// ── tests ──────────────────────────────────────────────────────────────

func TestServeE2E_AuthRejected(t *testing.T) {
	s := startServer(t)
	defer s.stop()

	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"})

	resp, err := http.Post(s.url(), "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("no-token want 401 got %d", resp.StatusCode)
	}

	req, _ := http.NewRequest("POST", s.url(), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("wrong-token want 401 got %d", resp.StatusCode)
	}
}

func TestServeE2E_HealthzNoAuth(t *testing.T) {
	s := startServer(t)
	defer s.stop()

	resp, err := http.Get("http://" + s.addr + "/healthz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("healthz status=%d", resp.StatusCode)
	}
}

func TestServeE2E_ToolsListExposesToolSurface(t *testing.T) {
	s := startServer(t)
	defer s.stop()
	sid := s.initSession(t)

	_, resp, _ := s.jsonRPC(t, sid, s.token, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list",
	})
	result := resp["result"].(map[string]any)
	tools := result["tools"].([]any)

	got := make(map[string]bool)
	for _, tool := range tools {
		got[tool.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"mem_save", "mem_search", "mem_context", "mem_get", "graph_symbol", "graph_package"} {
		if !got[want] {
			t.Errorf("missing tool: %s (have %v)", want, got)
		}
	}
	for _, hidden := range []string{"mem_list", "mem_schema", "mem_doctor"} {
		if got[hidden] {
			t.Errorf("hidden tool exposed: %s", hidden)
		}
	}
}

// TestServeE2E_InitializeExposesInstructions guards the ADR-0019 Layer-1 lever:
// the proactive protocol must reach the client via the initialize response, or
// non-Claude hosts get no nudge to call the tools on their own.
func TestServeE2E_InitializeExposesInstructions(t *testing.T) {
	s := startServer(t)
	defer s.stop()

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
	// Core tool nudge + the AI-first lifecycle read-side signals (ADR-0031):
	// pinned/needs_review/supersedes must reach the client or the flags are dead.
	for _, want := range []string{"mem_search", "mem_context", "mem_save", "pinned", "needs_review", "supersedes"} {
		if !strings.Contains(instr, want) {
			t.Errorf("initialize instructions missing %q; got %q", want, instr)
		}
	}
}

func TestServeE2E_ContextMintsSessionAndSaveReusesIt(t *testing.T) {
	s := startServer(t)
	defer s.stop()
	sid := s.initSession(t)

	ctxResp := s.callTool(t, sid, "mem_context", map[string]any{"task_type": "smoke"})
	mintedSession, _ := ctxResp["session_id"].(string)
	if !strings.HasPrefix(mintedSession, "sess_") {
		t.Fatalf("expected session_id with sess_ prefix, got %q", mintedSession)
	}
	if _, ok := ctxResp["context"].(map[string]any); !ok {
		t.Fatalf("mem_context envelope missing 'context' field; got %v", ctxResp)
	}

	saveResp := s.callTool(t, sid, "mem_save", map[string]any{
		"kind":       "task_pattern",
		"title":      "smoke save",
		"what":       "tested mcp bridge end to end",
		"learned":    "mcp bridge ok",
		"task_type":  "smoke",
		"session_id": mintedSession,
	})
	if saveResp["status"] != "saved" {
		t.Fatalf("first save status=%v, want saved; resp=%v", saveResp["status"], saveResp)
	}
	if got := saveResp["session_id"]; got != mintedSession {
		t.Fatalf("save returned session_id=%v, want %s", got, mintedSession)
	}
	if id, _ := saveResp["id"].(string); !strings.HasPrefix(id, "mem_") {
		t.Fatalf("expected memory id with mem_ prefix, got %q", id)
	}
}

func TestServeE2E_DuplicateSaveSkipped(t *testing.T) {
	s := startServer(t)
	defer s.stop()
	sid := s.initSession(t)

	args := map[string]any{
		"kind":      "error_resolution",
		"title":     "duplicate probe",
		"what":      "saved twice to assert dedupe",
		"learned":   "fingerprint should match on the second insert",
		"task_type": "dedupe",
	}
	first := s.callTool(t, sid, "mem_save", args)
	if first["status"] != "saved" {
		t.Fatalf("first save status=%v", first["status"])
	}
	second := s.callTool(t, sid, "mem_save", args)
	if second["status"] != "skipped" {
		t.Fatalf("second save status=%v, want skipped", second["status"])
	}
	if second["reason"] != "duplicate" {
		t.Fatalf("second save reason=%v, want duplicate", second["reason"])
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
