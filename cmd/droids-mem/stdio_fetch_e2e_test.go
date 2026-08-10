package main_test

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// seedSharedPool builds a real git pool holding one shared memory, the shape
// Fetch consumes.
//
// The pool is a CLONE of a bare origin, not a bare `git init`: fetchInto runs
// `git pull` unconditionally, so a pool without an upstream fails with "no
// tracking information" and never reaches the import. Cloning gives the branch
// real tracking info while keeping everything on local paths, so the test needs
// no network.
func seedSharedPool(t *testing.T, line string) string {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	pool := filepath.Join(root, "pool")

	git := func(dir string, args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git -C %s %v: %v: %s", dir, args, err, out)
		}
	}

	if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", origin).CombinedOutput(); err != nil {
		t.Fatalf("init bare origin: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "clone", "-q", origin, pool).CombinedOutput(); err != nil {
		t.Fatalf("clone pool: %v: %s", err, out)
	}
	git(pool, "config", "user.email", "t@example.com")
	git(pool, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(pool, "shared.jsonl"), []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(pool, "add", "shared.jsonl")
	git(pool, "commit", "-q", "-m", "seed")
	git(pool, "push", "-q", "-u", "origin", "main")
	return pool
}

// The shared-pool auto-Fetch (ADR-0029 §5) was wired to the HTTP Run only. If
// the HTTP transport goes away and this is not wired into RunStdio, the
// feature silently disappears during the migration: a teammate's memories stop
// arriving and nothing reports it.
//
// Measured basis for doing this per spawn rather than on a timer: this machine
// starts 1-19 Claude Code sessions a day (median ~5), and a fetch against an
// unreachable remote now costs ~2s of teardown at worst, so throttling would be
// machinery for a cost that is not there.
func TestE2E_StdioBootFetchImportsSharedPool(t *testing.T) {
	workDir := t.TempDir()
	dbPath := filepath.Join(workDir, "mem.db")
	pool := seedSharedPool(t,
		`{"kind":"task_pattern","task_type":"pool_probe","title":"Pooled lesson from a teammate","what":"context body for the pooled lesson","learned":"apply the pooled lesson next time","tags":"pool probe"}`)

	cmd := exec.Command(binaryPath, "serve", "--stdio")
	cmd.Env = append(os.Environ(),
		"DROIDS_MEM_DB="+dbPath,
		"DROIDS_MEM_HOME="+workDir,
		"DROIDS_MEM_SHARE_REPO="+pool,
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	reader := bufio.NewReader(stdout)
	if _, err := fmt.Fprintln(stdin, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"e2e","version":"0"}}}`); err != nil {
		t.Fatalf("write stdin: %v (stderr: %s)", err, stderr.String())
	}
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("read initialize: %v (stderr: %s)", err, stderr.String())
	}

	// Let the boot fetch land, then end the session the way a host does.
	time.Sleep(2 * time.Second)
	_ = stdin.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("stdio process did not exit on EOF; stderr: %s", stderr.String())
	}

	out := cli(t, dbPath, nil, "search", "--query", "pooled lesson teammate")
	var resp struct {
		Results []struct {
			Title string `json:"title"`
		} `json:"results"`
	}
	mustParseJSON(t, out, &resp)
	for _, r := range resp.Results {
		if r.Title == "Pooled lesson from a teammate" {
			return
		}
	}
	t.Errorf("stdio boot fetch did not import the shared pool; search returned %+v\nserver stderr: %s",
		resp.Results, stderr.String())
}
