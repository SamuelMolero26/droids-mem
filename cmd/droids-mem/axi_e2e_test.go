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

// cliStderr runs the binary and returns (stdout, stderr, exitCode). Unlike cli,
// it never fails the test on a non-zero exit — usage-error paths write JSON to
// stderr and exit 2, and the test asserts on that.
func cliStderr(t *testing.T, dbPath string, args ...string) ([]byte, []byte, int) {
	t.Helper()
	cmd := exec.Command(binaryPath, args...)
	cmd.Env = append(os.Environ(), "DROIDS_MEM_DB="+dbPath)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			t.Fatalf("cli %v: %v", args, err)
		}
	}
	return []byte(stdout.String()), []byte(stderr.String()), code
}

// AXI §6: an unknown flag must fail loud with a structured JSON envelope, not
// raw cobra text, and exit 2.
func TestE2E_UnknownFlagIsStructuredJSON(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mem.db")
	_, stderr, code := cliStderr(t, dbPath, "save", "--bogus", "1",
		"--task-type", "t", "--kind", "task_pattern",
		"--title", "x", "--what", "y", "--learned", "z")
	if code != 2 {
		t.Fatalf("unknown flag exit = %d, want 2", code)
	}
	var env struct {
		Status     string `json:"status"`
		Code       string `json:"code"`
		Suggestion string `json:"suggestion"`
	}
	if err := json.Unmarshal(stderr, &env); err != nil {
		t.Fatalf("stderr not JSON: %v\nraw: %s", err, stderr)
	}
	if env.Status != "error" || env.Code != "usage_error" || env.Suggestion == "" {
		t.Fatalf("want usage_error envelope with suggestion, got %+v", env)
	}
}

// AXI §8: a bare invocation shows live corpus content, not the usage banner.
func TestE2E_BareInvocationShowsContent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mem.db")
	stdout, _, code := cliStderr(t, dbPath)
	if code != 0 {
		t.Fatalf("bare invocation exit = %d, want 0", code)
	}
	var home struct {
		Bin   string   `json:"bin"`
		Total int      `json:"total"`
		Help  []string `json:"help"`
	}
	if err := json.Unmarshal(stdout, &home); err != nil {
		t.Fatalf("bare stdout not JSON content: %v\nraw: %s", err, stdout)
	}
	if home.Bin == "" || len(home.Help) == 0 {
		t.Fatalf("home view missing bin/help: %+v", home)
	}
}

// The MCP graph_package arg name (`--package`) must work on the CLI too, matching
// the positional form byte-for-byte in output (surface parity).
func TestE2E_GraphPackageFlagParity(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mem.db")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Dir(filepath.Dir(wd)) // test cwd is cmd/droids-mem; repo root is two up

	viaArg, _, c1 := cliStderr(t, dbPath, "graph", "package", "internal/store", "--repo", repo)
	viaFlag, _, c2 := cliStderr(t, dbPath, "graph", "package", "--package", "internal/store", "--repo", repo)
	if c1 != 0 || c2 != 0 {
		t.Fatalf("graph package exits: arg=%d flag=%d, want 0/0", c1, c2)
	}
	if len(viaArg) == 0 || string(viaArg) != string(viaFlag) {
		t.Fatalf("--package must match positional output\narg: %s\nflag: %s", viaArg, viaFlag)
	}
}
