package main_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// graphCLI runs a graph subcommand with an isolated state dir, so a test graph
// never lands in the developer's real ~/.droids-mem/graphs.
func graphCLI(t *testing.T, home string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(binaryPath, args...)
	cmd.Env = append(os.Environ(),
		"DROIDS_MEM_HOME="+home,
		"DROIDS_MEM_DB="+filepath.Join(home, "mem.db"),
	)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("graph %v: %v (stderr: %s)", args, err, stderr.String())
		}
		code = ee.ExitCode()
	}
	return stdout.String() + stderr.String(), code
}

func writeGoFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// brokenGoRepo returns a temp Go module that cannot type-check, so any graph
// build against it fails with the same underlying cause (repo failed to load
// or type-check) that issue #72's two error paths share.
func brokenGoRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	writeGoFile(t, filepath.Join(repo, "go.mod"), "module example.com/broken\n\ngo 1.21\n")
	writeGoFile(t, filepath.Join(repo, "broken.go"), "package broken\n\nfunc Undefined() {\n\tmissing\n}\n")
	return repo
}

// graphErrEnvelope runs a graph subcommand expected to fail and returns the
// parsed error envelope from stderr plus the exit code.
func graphErrEnvelope(t *testing.T, home string, args ...string) (struct {
	Code      string `json:"code"`
	Retryable bool   `json:"retryable"`
}, int) {
	t.Helper()
	cmd := exec.Command(binaryPath, args...)
	cmd.Env = append(os.Environ(),
		"DROIDS_MEM_HOME="+home,
		"DROIDS_MEM_DB="+filepath.Join(home, "mem.db"),
	)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("graph %v: %v (stderr: %s)", args, err, stderr.String())
		}
		code = ee.ExitCode()
	}
	var env struct {
		Code      string `json:"code"`
		Retryable bool   `json:"retryable"`
	}
	if err := json.Unmarshal([]byte(stderr.String()), &env); err != nil {
		t.Fatalf("parse error envelope: %v\nraw: %s\nstdout: %s", err, stderr.String(), stdout.String())
	}
	return env, code
}

// TestE2E_GraphRetryableConsistent pins issue #72: a graph build/type-check
// failure is retryable only after the repo is fixed, so BOTH error paths must
// report retryable:false. `graph index` historically said true while
// symbol/package said false for the same underlying cause.
func TestE2E_GraphRetryableConsistent(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain required to build a graph")
	}
	cases := []struct {
		name string
		args []string
		code string
	}{
		{"index", []string{"graph", "index"}, "graph_index_failed"},
		{"symbol", []string{"graph", "symbol", "Undefined"}, "graph_query_failed"},
		{"package", []string{"graph", "package", "broken"}, "graph_query_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			repo := brokenGoRepo(t)
			env, code := graphErrEnvelope(t, home, append(tc.args, "--repo", repo)...)
			if code == 0 {
				t.Fatalf("expected graph failure on broken repo, got exit 0")
			}
			if env.Code != tc.code {
				t.Errorf("error code = %q, want %q", env.Code, tc.code)
			}
			if env.Retryable {
				t.Errorf("%s: build failure must not be retryable — the repo must be fixed first (issue #72)", tc.name)
			}
		})
	}
}

// A one-shot CLI process exits the instant the command returns, so an async
// rebuild it triggered has nobody to hand its result to. Without waiting, a
// stale graph on the CLI path can never refresh: every invocation restarts a
// build that dies with the process (#70). The second query must answer from
// current source, not warm-serve the pre-edit graph forever.
func TestE2E_GraphCLIRefreshesStaleGraph(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain required to build a graph")
	}
	home := t.TempDir()
	repo := t.TempDir()
	pkgDir := filepath.Join(repo, "pkg")
	if err := os.MkdirAll(pkgDir, 0o750); err != nil {
		t.Fatal(err)
	}
	writeGoFile(t, filepath.Join(repo, "go.mod"), "module example.com/graphtest\n\ngo 1.21\n")
	writeGoFile(t, filepath.Join(pkgDir, "a.go"), "package pkg\n\nfunc Alpha() {}\n")

	out, code := graphCLI(t, home, "graph", "package", "pkg", "--repo", repo)
	if code != 0 {
		t.Fatalf("first query exit = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "Alpha") {
		t.Fatalf("first query missing Alpha:\n%s", out)
	}

	// Change the source: the stamp moves, so the cached graph is now stale.
	writeGoFile(t, filepath.Join(pkgDir, "a.go"), "package pkg\n\nfunc Alpha() {}\n\nfunc Beta() {}\n")

	out, code = graphCLI(t, home, "graph", "package", "pkg", "--repo", repo)
	if code != 0 {
		t.Fatalf("second query exit = %d, want 0\n%s", code, out)
	}
	if strings.Contains(out, "STALE") || strings.Contains(out, "REBUILDING") {
		t.Fatalf("CLI served a stale graph instead of waiting for its own rebuild:\n%s", out)
	}
	if !strings.Contains(out, "Beta") {
		t.Fatalf("second query missing Beta — graph never refreshed:\n%s", out)
	}
}
