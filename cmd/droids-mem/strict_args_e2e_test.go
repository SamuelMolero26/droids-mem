package main_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2E_StrictToolArgs (issue #228) proves tool arguments are validated
// against each tool's input schema: unknown keys and fractional integers are
// rejected before the handler runs, and integer fields accept every integral
// JSON encoding. It runs over HTTP only: both transports share one server
// constructor (newMCPServer).
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

	s := startServer(t)
	defer s.stop()
	sid := s.initSession(t)

	t.Run("mem_save dryRun is rejected and writes nothing", func(t *testing.T) {
		isErr, text := s.callToolRaw(t, sid, "mem_save",
			`{"kind":"task_pattern","title":"misspelled preview","what":"w","learned":"l","task_type":"strict","dryRun":true}`)
		if !isErr || !strings.Contains(text, "dryRun") {
			t.Errorf("isError=%v text=%q, want a tool error naming dryRun", isErr, text)
		}
		if total := s.callTool(t, sid, "mem_corpus", map[string]any{})["total"]; total != float64(0) {
			t.Errorf("corpus total = %v after rejected save, want 0", total)
		}
	})

	// Warm the graph so graph_symbol answers from a built index.
	s.callToolRaw(t, sid, "graph_build_wait", `{"repo":`+r+`}`)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isErr, text := s.callToolRaw(t, sid, tc.tool, tc.args)
			if isErr != tc.wantErr {
				t.Fatalf("isError = %v, want %v; text=%q", isErr, tc.wantErr, text)
			}
			if tc.wantErr && !strings.Contains(text, tc.wantText) {
				t.Errorf("text = %q, want it to contain %q", text, tc.wantText)
			}
		})
	}
}
