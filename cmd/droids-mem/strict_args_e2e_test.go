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

// rpcFunc sends one JSON-RPC request with raw params and returns the decoded
// response. Params stay raw text so encodings like 5.0 and 5e0 reach the
// server verbatim (json.Marshal of float64 5.0 would emit 5).
type rpcFunc func(t *testing.T, method, params string) map[string]any

func httpRPC(t *testing.T) rpcFunc {
	t.Helper()
	s := startServer(t)
	t.Cleanup(s.stop)
	sid := s.initSession(t)
	return func(t *testing.T, method, params string) map[string]any {
		t.Helper()
		_, resp, _ := s.jsonRPC(t, sid, s.token, map[string]any{
			"jsonrpc": "2.0", "id": time.Now().UnixNano(), "method": method,
			"params": json.RawMessage(params),
		})
		return resp
	}
}

func stdioRPC(t *testing.T) rpcFunc {
	t.Helper()
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
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })

	reader := bufio.NewReader(stdout)
	id := 0
	rpc := func(t *testing.T, method, params string) map[string]any {
		t.Helper()
		id++
		if _, err := fmt.Fprintf(stdin, `{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`+"\n", id, method, params); err != nil {
			t.Fatalf("write stdin: %v (stderr: %s)", err, stderr.String())
		}
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatalf("read stdout: %v (stderr: %s)", err, stderr.String())
			}
			var resp map[string]any
			if err := json.Unmarshal([]byte(line), &resp); err != nil {
				t.Fatalf("parse %q: %v", line, err)
			}
			if resp["id"] != nil { // skip server notifications
				return resp
			}
		}
	}
	rpc(t, "initialize", `{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"e2e","version":"0"}}`)
	if _, err := fmt.Fprintln(stdin, `{"jsonrpc":"2.0","method":"notifications/initialized"}`); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	return rpc
}

// callToolRaw returns the tool result's isError flag and first text block.
func callToolRaw(t *testing.T, rpc rpcFunc, name, args string) (bool, string) {
	t.Helper()
	resp := rpc(t, "tools/call", fmt.Sprintf(`{"name":%q,"arguments":%s}`, name, args))
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

// TestE2E_StrictToolArgs (issue #228) proves tool arguments are validated
// against each tool's input schema on both transports: unknown keys and
// fractional integers are rejected before the handler runs, and integer
// fields accept every integral JSON encoding.
func TestE2E_StrictToolArgs(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "pkg"), 0o750); err != nil {
		t.Fatal(err)
	}
	writeGoFile(t, filepath.Join(repo, "go.mod"), "module example.com/strict\n\ngo 1.21\n")
	writeGoFile(t, filepath.Join(repo, "pkg", "a.go"), "package pkg\n\nfunc Alpha() {}\n")
	repoJSON, _ := json.Marshal(repo)
	r := string(repoJSON)

	type argCase struct {
		name, tool, args string
		wantErr          bool
		wantText         string // substring of the result text when wantErr
	}
	cases := []argCase{
		{"mem_search taskType", "mem_search", `{"query":"x","taskType":"nonexistent"}`, true, "taskType"},
		{"graph_build_wait timeout_seconds", "graph_build_wait", `{"repo":` + r + `,"timeout_seconds":5}`, true, "timeout_seconds"},
		{"mem_save missing required", "mem_save", `{"kind":"task_pattern","title":"t","task_type":"strict","dry_run":true}`, true, "what"},
		{"mem_search limit over max", "mem_search", `{"query":"x","limit":50}`, true, "limit"},
	}
	unknownKey := map[string]string{
		"mem_save":         `{"kind":"task_pattern","title":"t","what":"w","learned":"l","task_type":"strict","dry_run":true,"bogus_key":1}`,
		"mem_search":       `{"query":"x","bogus_key":1}`,
		"mem_context":      `{"task_type":"strict","bogus_key":1}`,
		"mem_get":          `{"id":"01ARZ3NDEKTSV4RRFFQ69G5FAV","bogus_key":1}`,
		"mem_corpus":       `{"bogus_key":1}`,
		"graph_symbol":     `{"repo":` + r + `,"symbol":"Alpha","bogus_key":1}`,
		"graph_package":    `{"repo":` + r + `,"package":"pkg","bogus_key":1}`,
		"graph_build_wait": `{"repo":` + r + `,"bogus_key":1}`,
	}
	for tool, args := range unknownKey {
		cases = append(cases, argCase{tool + " unknown key", tool, args, true, "bogus_key"})
	}
	// Each integer field as a format template taking the raw number literal.
	intFields := []struct{ tool, args string }{
		{"mem_search", `{"query":"x","limit":%s}`},
		{"mem_corpus", `{"limit":%s}`},
		{"graph_symbol", `{"repo":` + r + `,"symbol":"Alpha","depth":%s}`},
		{"graph_build_wait", `{"repo":` + r + `,"timeout":%s}`},
	}
	for _, f := range intFields {
		for _, n := range []string{"5", "5.0", "5e0"} {
			cases = append(cases, argCase{f.tool + " integer " + n, f.tool, fmt.Sprintf(f.args, n), false, ""})
		}
		cases = append(cases, argCase{f.tool + " fractional 2.5", f.tool, fmt.Sprintf(f.args, "2.5"), true, "integer"})
	}

	for _, tr := range []struct {
		name  string
		start func(*testing.T) rpcFunc
	}{{"http", httpRPC}, {"stdio", stdioRPC}} {
		t.Run(tr.name, func(t *testing.T) {
			rpc := tr.start(t)

			t.Run("tools/list advertises strict schemas", func(t *testing.T) {
				resp := rpc(t, "tools/list", `{}`)
				result, _ := resp["result"].(map[string]any)
				list, _ := result["tools"].([]any)
				schemas := map[string]map[string]any{}
				for _, tl := range list {
					m, _ := tl.(map[string]any)
					name, _ := m["name"].(string)
					schemas[name], _ = m["inputSchema"].(map[string]any)
				}
				for tool := range unknownKey {
					s, ok := schemas[tool]
					if !ok {
						t.Errorf("tools/list missing %s", tool)
						continue
					}
					if s["additionalProperties"] != false {
						t.Errorf("%s additionalProperties = %v, want false", tool, s["additionalProperties"])
					}
				}
				for tool, field := range map[string]string{"mem_search": "limit", "mem_corpus": "limit", "graph_symbol": "depth", "graph_build_wait": "timeout"} {
					props, _ := schemas[tool]["properties"].(map[string]any)
					p, _ := props[field].(map[string]any)
					if p["type"] != "integer" {
						t.Errorf("%s.%s type = %v, want integer", tool, field, p["type"])
					}
				}
			})

			t.Run("mem_save dryRun is rejected and writes nothing", func(t *testing.T) {
				isErr, text := callToolRaw(t, rpc, "mem_save",
					`{"kind":"task_pattern","title":"misspelled preview","what":"w","learned":"l","task_type":"strict","dryRun":true}`)
				if !isErr || !strings.Contains(text, "dryRun") {
					t.Errorf("isError=%v text=%q, want a tool error naming dryRun", isErr, text)
				}
				_, corpus := callToolRaw(t, rpc, "mem_corpus", `{}`)
				var c struct {
					Total int `json:"total"`
				}
				if err := json.Unmarshal([]byte(corpus), &c); err != nil {
					t.Fatalf("mem_corpus: %v\nraw: %s", err, corpus)
				}
				if c.Total != 0 {
					t.Errorf("corpus total = %d after rejected save, want 0", c.Total)
				}
			})

			// Warm the graph so graph_symbol answers from a built index.
			callToolRaw(t, rpc, "graph_build_wait", `{"repo":`+r+`}`)

			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					isErr, text := callToolRaw(t, rpc, tc.tool, tc.args)
					if isErr != tc.wantErr {
						t.Fatalf("isError = %v, want %v; text=%q", isErr, tc.wantErr, text)
					}
					if tc.wantErr && !strings.Contains(text, tc.wantText) {
						t.Errorf("text = %q, want it to contain %q", text, tc.wantText)
					}
				})
			}
		})
	}
}
