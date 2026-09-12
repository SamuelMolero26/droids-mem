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
	"syscall"
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

// identityServer stands in for a live daemon. provenPid < 0 emits no
// pid_proof at all, modelling a server built before PID binding.
func identityServer(t *testing.T, token string, provenPid int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce := r.URL.Query().Get("nonce")
		if provenPid < 0 {
			fmt.Fprintf(w, `{"server":%q,"proof":%q}`,
				mcpserver.ServerName, mcpserver.IdentityProof(token, nonce))
			return
		}
		fmt.Fprintf(w, `{"server":%q,"proof":%q,"pid":%d,"pid_proof":%q}`,
			mcpserver.ServerName, mcpserver.IdentityProof(token, nonce),
			provenPid, mcpserver.IdentityPidProof(token, nonce, provenPid))
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// sleeper is a real, signallable stand-in for the daemon process.
func sleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	c := exec.Command("sleep", "60")
	if err := c.Start(); err != nil {
		t.Fatalf("start sleeper: %v", err)
	}
	t.Cleanup(func() { _ = c.Process.Kill() })
	return c
}

func TestStopServerStatus_StopsVerifiedServer(t *testing.T) {
	pidPath := stopServerEnv(t)
	victim := sleeper(t)
	t.Setenv("DROIDS_MEM_MCP_ADDR", identityServer(t, "tok_test", victim.Process.Pid))

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

// The gap a token-only challenge leaves open: a server that holds the token is
// listening, but the pidfile names a different process the OS handed that PID
// to after the recorded daemon died. Signalling here kills a stranger.
func TestStopServerStatus_RefusesPidMismatch(t *testing.T) {
	pidPath := stopServerEnv(t)
	bystander := sleeper(t)
	// The listener proves it is some other process, not the recorded one.
	t.Setenv("DROIDS_MEM_MCP_ADDR", identityServer(t, "tok_test", bystander.Process.Pid+100000))

	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(bystander.Process.Pid)), 0o600); err != nil {
		t.Fatalf("write pidfile: %v", err)
	}

	got := stopServerStatus()
	if !strings.HasPrefix(got, "not_verified") {
		t.Fatalf("stopServerStatus() = %q, want not_verified (listener is a different process)", got)
	}
	if err := bystander.Process.Signal(syscall.Signal(0)); err != nil {
		t.Errorf("bystander should be untouched, but is gone: %v", err)
	}
	if _, err := os.Stat(pidPath); err != nil {
		t.Errorf("pidfile should survive a refusal: %v", err)
	}
}

// A daemon built before PID binding proves the token but not which process it
// is. Unproven is not signalled — the daemon is left for the user to stop.
func TestStopServerStatus_RefusesUnprovenPid(t *testing.T) {
	pidPath := stopServerEnv(t)
	bystander := sleeper(t)
	t.Setenv("DROIDS_MEM_MCP_ADDR", identityServer(t, "tok_test", -1))

	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(bystander.Process.Pid)), 0o600); err != nil {
		t.Fatalf("write pidfile: %v", err)
	}

	got := stopServerStatus()
	if !strings.Contains(got, "predates PID binding") {
		t.Fatalf("stopServerStatus() = %q, want a not_verified status naming the unproven PID", got)
	}
	if err := bystander.Process.Signal(syscall.Signal(0)); err != nil {
		t.Errorf("bystander should be untouched, but is gone: %v", err)
	}
}

// A relaying squatter can forward the real server's nonce to obtain a valid
// token proof, then claim whatever PID it wants. Binding the PID into its own
// HMAC is what makes that claim uncheckable to forge.
func TestStopServerStatus_RejectsForgedPidProof(t *testing.T) {
	pidPath := stopServerEnv(t)
	victim := sleeper(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce := r.URL.Query().Get("nonce")
		// Valid token proof, but the pid_proof is computed over a PID the
		// forger does not actually hold the binding for.
		fmt.Fprintf(w, `{"server":%q,"proof":%q,"pid":%d,"pid_proof":%q}`,
			mcpserver.ServerName, mcpserver.IdentityProof("tok_test", nonce),
			victim.Process.Pid, mcpserver.IdentityPidProof("wrong-token", nonce, victim.Process.Pid))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("DROIDS_MEM_MCP_ADDR", strings.TrimPrefix(srv.URL, "http://"))

	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(victim.Process.Pid)), 0o600); err != nil {
		t.Fatalf("write pidfile: %v", err)
	}

	if got := stopServerStatus(); !strings.HasPrefix(got, "not_verified") {
		t.Fatalf("stopServerStatus() = %q, want not_verified on a forged pid proof", got)
	}
	if err := victim.Process.Signal(syscall.Signal(0)); err != nil {
		t.Errorf("victim should be untouched, but is gone: %v", err)
	}
}
