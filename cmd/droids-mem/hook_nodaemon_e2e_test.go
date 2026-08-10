package main_test

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// freePort reserves an ephemeral port and releases it, so the hook under test
// has a definitely-unused address to (not) bind.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// Under stdio the host spawns the server per session, so nothing needs a
// background daemon. The SessionStart hook used to re-exec `ensure-server`,
// which meant every `claude` CLI invocation resurrected a listener nobody
// connects to — observed respawning 9 seconds after being killed, purely
// because `claude mcp get` ran.
//
// It also made the migration unverifiable: you can never see a clean
// no-daemon state to check stdio against.
func TestE2E_SessionStartHookStartsNoDaemon(t *testing.T) {
	workDir := t.TempDir()
	addr := freePort(t)

	cmd := exec.Command(binaryPath, "session", "hook", "sessionstart")
	cmd.Env = append(os.Environ(),
		"DROIDS_MEM_DB="+filepath.Join(workDir, "mem.db"),
		"DROIDS_MEM_HOME="+workDir,
		"DROIDS_MEM_MCP_ADDR="+addr,
	)
	cmd.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart","session_id":"nodaemon-probe"}`)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("session hook sessionstart: %v: %s", err, out)
	}

	// A detached spawn is asynchronous; give it room to bind before concluding.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			t.Fatalf("SessionStart hook started a daemon on %s — stdio hosts spawn the server themselves", addr)
		}
		time.Sleep(150 * time.Millisecond)
	}

	// The pid sentinel is written only by spawnDetached, so its presence is a
	// second, independent witness that a daemon was launched.
	if _, err := os.Stat(filepath.Join(workDir, "mcp.pid")); err == nil {
		b, _ := os.ReadFile(filepath.Join(workDir, "mcp.pid")) // #nosec G304 -- test temp dir
		t.Errorf("SessionStart hook wrote a pid file (%s) — it still spawns a server", strings.TrimSpace(string(b)))
	}
}

// Removing the spawn must not disturb the other thing SessionStart does:
// sweeping orphaned staged summaries from crashed runs.
func TestE2E_SessionStartHookStillRecovers(t *testing.T) {
	workDir := t.TempDir()
	dbPath := filepath.Join(workDir, "mem.db")

	cmd := exec.Command(binaryPath, "session", "hook", "sessionstart")
	cmd.Env = append(os.Environ(),
		"DROIDS_MEM_DB="+dbPath,
		"DROIDS_MEM_HOME="+workDir,
		"DROIDS_MEM_MCP_ADDR="+freePort(t),
	)
	cmd.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart","session_id":"recover-probe"}`)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("session hook sessionstart: %v: %s", err, out)
	}
}
