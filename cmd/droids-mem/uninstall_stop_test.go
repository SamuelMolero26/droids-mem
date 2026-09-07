package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/samuelmolero26/droids-mem/internal/mcpserver"
	"github.com/samuelmolero26/droids-mem/internal/state"
)

// stopServerEnv points the state dir at a temp dir and pins the token so the
// identity challenge is deterministic. Returns the pidfile path.
func stopServerEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("DROIDS_MEM_HOME", home)
	t.Setenv("DROIDS_MEM_MCP_TOKEN", "tok_test")
	return filepath.Join(home, state.PidFile)
}

// identityServer answers /identity with a valid HMAC proof for token, and
// points DROIDS_MEM_MCP_ADDR at itself so stopServerStatus probes it.
func identityServer(t *testing.T, token string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce := r.URL.Query().Get("nonce")
		fmt.Fprintf(w, `{"server":%q,"proof":%q}`, mcpserver.ServerName, mcpserver.IdentityProof(token, nonce))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("DROIDS_MEM_MCP_ADDR", strings.TrimPrefix(srv.URL, "http://"))
}

func TestStopServerStatus_NoPidfile(t *testing.T) {
	stopServerEnv(t)

	if got := stopServerStatus(); got != "not_running" {
		t.Errorf("stopServerStatus() = %q, want not_running", got)
	}
}

// TestStopServerStatus_RefusesUnverifiedPid is the whole point of the identity
// probe: a stale pidfile whose PID the OS has recycled must not be signalled.
// The pidfile here names THIS test process, so a regression that signals
// blindly kills the test runner instead of failing an assertion.
func TestStopServerStatus_RefusesUnverifiedPid(t *testing.T) {
	pidPath := stopServerEnv(t)
	// No listener on this address, so the identity probe cannot succeed.
	t.Setenv("DROIDS_MEM_MCP_ADDR", "127.0.0.1:1")

	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatalf("write pidfile: %v", err)
	}

	got := stopServerStatus()
	if !strings.HasPrefix(got, "not_verified") {
		t.Errorf("stopServerStatus() = %q, want a not_verified status (must not signal an unverified PID)", got)
	}
	// The pidfile survives: we did not prove anything about that process, so
	// removing its record would just hide the inconsistency from the user.
	if _, err := os.Stat(pidPath); err != nil {
		t.Errorf("pidfile should be left intact when unverified: %v", err)
	}
}

func TestStopServerStatus_StopsVerifiedServer(t *testing.T) {
	pidPath := stopServerEnv(t)
	identityServer(t, "tok_test")

	// A real, signallable stand-in for the daemon.
	victim := exec.Command("sleep", "60")
	if err := victim.Start(); err != nil {
		t.Fatalf("start victim: %v", err)
	}
	t.Cleanup(func() { _ = victim.Process.Kill() })

	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(victim.Process.Pid)), 0o600); err != nil {
		t.Fatalf("write pidfile: %v", err)
	}

	if got := stopServerStatus(); got != "stopped" {
		t.Fatalf("stopServerStatus() = %q, want stopped", got)
	}
	if err := victim.Wait(); err == nil {
		t.Error("victim exited cleanly, want termination by signal")
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("pidfile should be cleared after a successful stop")
	}
}
