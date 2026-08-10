package main_test

import (
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
