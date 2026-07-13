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
	"time"
)

// TestE2E_ServeStdio drives `serve --stdio` over its stdin/stdout pipe:
// initialize must return the stdio instructions variant (self-save summary
// protocol), tools/list the full 6-tool surface, and closing stdin must end
// the process cleanly (host-managed lifecycle).
func TestE2E_ServeStdio(t *testing.T) {
	workDir := t.TempDir()
	cmd := exec.Command(binaryPath, "serve", "--stdio")
	cmd.Env = append(os.Environ(),
		"DROIDS_MEM_DB="+filepath.Join(workDir, "mem.db"),
		"DROIDS_MEM_HOME="+workDir,
	)
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
	init := recv()
	result, _ := init["result"].(map[string]any)
	if result == nil {
		t.Fatalf("initialize returned no result: %v", init)
	}
	instr, _ := result["instructions"].(string)
	if !strings.Contains(instr, "AT THE END of a run") {
		t.Errorf("stdio instructions missing self-save summary protocol; got %q", instr)
	}
	if strings.Contains(instr, "Do NOT save session summaries") {
		t.Errorf("stdio instructions carry the HTTP summary policy")
	}

	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools := recv()
	toolsResult, _ := tools["result"].(map[string]any)
	list, _ := toolsResult["tools"].([]any)
	names := map[string]bool{}
	for _, tl := range list {
		if m, ok := tl.(map[string]any); ok {
			if n, _ := m["name"].(string); n != "" {
				names[n] = true
			}
		}
	}
	for _, want := range []string{"mem_save", "mem_search", "mem_context", "mem_get", "graph_symbol", "graph_package"} {
		if !names[want] {
			t.Errorf("tools/list missing %s (got %v)", want, names)
		}
	}

	// Host-managed lifecycle: stdin EOF ends the process.
	_ = stdin.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		// exit after EOF is the contract; exit code varies by transport impl.
	case <-time.After(5 * time.Second):
		t.Fatalf("process did not exit on stdin EOF; stderr: %s", stderr.String())
	}
}
