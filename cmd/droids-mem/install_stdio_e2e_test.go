package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeClaude puts a stub `claude` first on PATH that appends its argv to a log
// and exits 0, except for `mcp get` which exits 1 so the caller treats the
// server as not-yet-registered. Returns the log path and the PATH-prefixed env.
func fakeClaude(t *testing.T, home string) (logPath string, env []string) {
	t.Helper()
	binDir := t.TempDir()
	logPath = filepath.Join(binDir, "argv.log")

	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + logPath + "\n" +
		"case \"$1 $2\" in 'mcp get') exit 1 ;; esac\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o700); err != nil { // #nosec G306 -- test stub must be executable
		t.Fatal(err)
	}

	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "HOME=") && !strings.HasPrefix(kv, "PATH=") {
			env = append(env, kv)
		}
	}
	env = append(env, "HOME="+home, "PATH="+binDir+":"+os.Getenv("PATH"))
	return logPath, env
}

func runWithEnv(t *testing.T, env []string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(binaryPath, args...)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run %v: %v", args, err)
	}
	return out
}

// The Claude Code registration must use the stdio transport: the host spawns
// the server as a child and owns its lifecycle, so there is no port to find, no
// daemon to keep alive, and no bearer token to leak. A registration that still
// names http:// would drag the whole daemon back in.
func TestE2E_InstallRegistersStdioNotHTTP(t *testing.T) {
	home := t.TempDir()
	logPath, env := fakeClaude(t, home)

	out := runWithEnv(t, env, "install", "--all")

	var resp struct {
		MCPRegistration string `json:"mcp_registration"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("parse install output: %v\nraw: %s", err, out)
	}
	if resp.MCPRegistration != "ok" {
		t.Fatalf("mcp_registration = %q, want ok", resp.MCPRegistration)
	}

	b, err := os.ReadFile(logPath) // #nosec G304 -- test-local temp path
	if err != nil {
		t.Fatalf("read claude argv log: %v", err)
	}
	log := string(b)

	var addLine string
	for line := range strings.SplitSeq(log, "\n") {
		if strings.HasPrefix(line, "mcp add") {
			addLine = line
		}
	}
	if addLine == "" {
		t.Fatalf("no `claude mcp add` invocation recorded:\n%s", log)
	}

	for _, want := range []string{"--scope user", "serve --stdio"} {
		if !strings.Contains(addLine, want) {
			t.Errorf("registration missing %q:\n%s", want, addLine)
		}
	}
	for _, forbidden := range []string{"--transport http", "http://", "Authorization", "Bearer"} {
		if strings.Contains(addLine, forbidden) {
			t.Errorf("registration still carries HTTP-transport artefact %q:\n%s", forbidden, addLine)
		}
	}
}

// A host that spawns the server per session needs no background daemon, so the
// bootstrap must not start one — that is the whole point of the migration.
func TestE2E_InstallAllDoesNotStartADaemon(t *testing.T) {
	home := t.TempDir()
	_, env := fakeClaude(t, home)

	out := runWithEnv(t, env, "install", "--all")

	var resp map[string]any
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("parse install output: %v\nraw: %s", err, out)
	}
	if _, present := resp["server"]; present {
		t.Errorf("install --all still reports a `server` bootstrap step: %v", resp["server"])
	}
}
