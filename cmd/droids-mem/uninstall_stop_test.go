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

// identityServer stands in for a live daemon on a throwaway address.
// proofToken is the token its pid_proof is computed with; "" omits the field
// entirely, modelling a server built before PID binding.
func identityServer(t *testing.T, pid int, proofToken string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce := r.URL.Query().Get("nonce")
		proof := mcpserver.IdentityProof("tok_test", nonce)
		if proofToken == "" {
			fmt.Fprintf(w, `{"server":%q,"proof":%q}`, mcpserver.ServerName, proof)
			return
		}
		fmt.Fprintf(w, `{"server":%q,"proof":%q,"pid":%d,"pid_proof":%q}`,
			mcpserver.ServerName, proof, pid, mcpserver.IdentityPidProof(proofToken, nonce, pid))
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// Every case here has a live token-holding listener and a pidfile, and differs
// only in what the listener can prove about which process it is. Holding the
// token is not enough: the pidfile is written solely by ensure-server's spawn,
// so a server started any other way can answer the challenge while the recorded
// PID has been recycled to something unrelated.
//
// The liveness assertion is the load-bearing one — a regression that signals
// blindly kills the stand-in process rather than merely returning a wrong
// string.
func TestStopServerStatus_SignalsOnlyAProvenPid(t *testing.T) {
	tests := []struct {
		name       string
		pidOffset  int    // added to the stand-in's real PID to form the proven one
		proofToken string // token behind pid_proof; "" omits the field
		wantStatus string
		wantKilled bool
	}{
		{
			name:       "proven pid is stopped",
			proofToken: "tok_test",
			wantStatus: "stopped",
			wantKilled: true,
		},
		{
			name:       "listener proves a different pid",
			pidOffset:  100000,
			proofToken: "tok_test",
			wantStatus: "not_verified",
		},
		{
			name:       "listener predates pid binding and proves none",
			proofToken: "",
			wantStatus: "predates PID binding",
		},
		{
			// A relaying squatter can forward the nonce for a genuine token
			// proof, then claim any PID. Binding the PID into its own HMAC is
			// what makes that claim unforgeable.
			name:       "pid proof is forged with another token",
			proofToken: "wrong-token",
			wantStatus: "not_verified",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pidPath := stopServerEnv(t)
			victim := exec.Command("sleep", "60")
			if err := victim.Start(); err != nil {
				t.Fatalf("start stand-in: %v", err)
			}
			t.Cleanup(func() { _ = victim.Process.Kill() })

			t.Setenv("DROIDS_MEM_MCP_ADDR", identityServer(t, victim.Process.Pid+tc.pidOffset, tc.proofToken))
			if err := os.WriteFile(pidPath, []byte(strconv.Itoa(victim.Process.Pid)), 0o600); err != nil {
				t.Fatalf("write pidfile: %v", err)
			}

			got := stopServerStatus()
			if !strings.Contains(got, tc.wantStatus) {
				t.Fatalf("stopServerStatus() = %q, want it to contain %q", got, tc.wantStatus)
			}

			// Wait, not a signal-0 probe: a terminated child lingers as a
			// zombie until it is reaped, and a probe reads that as alive.
			if tc.wantKilled {
				if err := victim.Wait(); err == nil {
					t.Error("stand-in exited cleanly, want termination by signal")
				}
			} else if err := victim.Process.Signal(syscall.Signal(0)); err != nil {
				t.Errorf("stand-in should be untouched, but is gone: %v", err)
			}
			// The pidfile is cleared only by a real stop. On a refusal it must
			// survive: it is the only evidence of the inconsistency.
			_, statErr := os.Stat(pidPath)
			if gone := os.IsNotExist(statErr); gone != tc.wantKilled {
				t.Errorf("pidfile gone = %v, want %v", gone, tc.wantKilled)
			}
		})
	}
}
