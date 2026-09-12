package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDecideStale(t *testing.T) {
	tests := []struct {
		name    string
		running serverIdentity
		want    string
		action  staleAction
		why     string
	}{
		{
			name:    "same version is left alone",
			running: serverIdentity{Version: "v1.2.3", Pid: 4242},
			want:    "v1.2.3",
			action:  keepServer,
			why:     "the daemon already runs this build",
		},
		{
			name:    "older version with a proven pid is replaced",
			running: serverIdentity{Version: "v1.2.2", Pid: 4242},
			want:    "v1.2.3",
			action:  replaceServer,
			why:     "stale, and it proved which process to signal",
		},
		{
			name:    "newer version is also replaced",
			running: serverIdentity{Version: "v2.0.0", Pid: 4242},
			want:    "v1.2.3",
			action:  replaceServer,
			why: "the daemon must match the binary being run, not merely be recent: " +
				"a downgrade leaves code the caller did not build",
		},
		{
			// The transition case. Replacing it would mean signalling a PID
			// read from a file rather than one proven over the wire, which is
			// the exact defect the PID binding closed.
			name:    "stale but unproven pid is reported, never signalled",
			running: serverIdentity{Version: "", Pid: 0},
			want:    "v1.2.3",
			action:  reportStale,
			why:     "a daemon predating PID binding cannot say which process it is",
		},
		{
			name:    "unproven pid on a matching version is still left alone",
			running: serverIdentity{Version: "v1.2.3", Pid: 0},
			want:    "v1.2.3",
			action:  keepServer,
			why:     "nothing to replace, so the missing proof does not matter",
		},
		{
			// Two local builds both report "dev". Treating that as stale would
			// restart the daemon on every single ensure-server call.
			name:    "dev against dev is not stale",
			running: serverIdentity{Version: "dev", Pid: 4242},
			want:    "dev",
			action:  keepServer,
			why:     "identical versions, however unspecific, mean the same build",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := decideStale(tc.running, tc.want); got != tc.action {
				t.Errorf("decideStale(%+v, %q) = %v, want %v — %s",
					tc.running, tc.want, got, tc.action, tc.why)
			}
		})
	}
}

// stopStale's contract is "the address is free when this returns nil", and the
// caller spawns a replacement on the strength of it. A failed signal does not
// establish that: the process may still be alive and holding the listener, in
// which case the replacement cannot bind, the health poll answers from the
// *stale* daemon, and ensure-server reports a restart that never happened.
//
// So the postcondition must be observed, never inferred from the signal.
func TestStopStale_FailedSignalDoesNotImplyFreeAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(srv.Close)

	// A PID the OS refuses to signal, while the listener is demonstrably up.
	err := stopStale(-1, srv.URL+"/healthz", 20*time.Millisecond, 200*time.Millisecond)
	if err == nil {
		t.Fatal("stopStale returned nil while the listener was still answering: " +
			"the caller would now spawn a replacement that cannot bind")
	}
}

// The legitimate case the failed-signal branch was written for: the daemon is
// already gone, so nothing answers and there is nothing to wait for.
func TestStopStale_AlreadyGoneReturnsPromptly(t *testing.T) {
	// Nothing listens here, so the first probe settles it.
	if err := stopStale(-1, "http://127.0.0.1:1/healthz", 20*time.Millisecond, 2*time.Second); err != nil {
		t.Fatalf("stopStale = %v, want nil when the address is already free", err)
	}
}
