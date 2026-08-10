package main_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2E_StatuslineBadgeAfterMCPGraphCall proves the indicator end to end over
// the path that matters: an agent calls graph_symbol through MCP, and a
// separate `droids-mem statusline` process reports it. The two share only the
// state dir, which is the whole point — the badge reflects any agent talking to
// this droids-mem, not the process that printed it.
func TestE2E_StatuslineBadgeAfterMCPGraphCall(t *testing.T) {
	workDir := t.TempDir()
	env := append(os.Environ(),
		"DROIDS_MEM_DB="+filepath.Join(workDir, "mem.db"),
		"DROIDS_MEM_HOME="+workDir,
	)

	statusline := func() string {
		t.Helper()
		c := exec.Command(binaryPath, "statusline")
		c.Env = env
		out, err := c.Output()
		if err != nil {
			t.Fatalf("statusline: %v", err)
		}
		return strings.TrimSpace(string(out))
	}

	// Nothing has queried the graph yet, so the badge must stay quiet.
	if got := statusline(); got != "" {
		t.Fatalf("idle statusline = %q, want empty", got)
	}

	// The badge marks that a graph tool RAN, not that it found anything, so an
	// empty repo is enough — and it keeps the test off the go toolchain.
	repo := t.TempDir()

	cmd := exec.Command(binaryPath, "serve", "--stdio")
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	reader := bufio.NewReader(stdout)
	send := func(msg string) {
		t.Helper()
		if _, err := fmt.Fprintln(stdin, msg); err != nil {
			t.Fatalf("write stdin: %v (stderr: %s)", err, stderr.String())
		}
	}
	recv := func() map[string]any {
		t.Helper()
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read stdout: %v (stderr: %s)", err, stderr.String())
		}
		var resp map[string]any
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("parse %q: %v", line, err)
		}
		return resp
	}

	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"e2e","version":"0"}}}`)
	recv()
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)

	call, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{
			"name":      "graph_symbol",
			"arguments": map[string]any{"repo": repo, "symbol": "Greet"},
		},
	})
	if err != nil {
		t.Fatalf("marshal call: %v", err)
	}
	send(string(call))
	if resp := recv(); resp["result"] == nil && resp["error"] == nil {
		t.Fatalf("graph_symbol returned neither result nor error: %v", resp)
	}

	// Whether the graph resolved the symbol or was still warming up, the tool
	// ran — and that is exactly what the badge claims.
	if got := statusline(); got != "droids-mem:graph_symbol" {
		t.Errorf("statusline = %q, want droids-mem:graph_symbol (stderr: %s)", got, stderr.String())
	}
}
