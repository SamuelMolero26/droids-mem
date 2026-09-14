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
	}{
		{
			// Also covers two local builds that both report "dev": equal
			// versions, however unspecific, are the same build.
			name:    "same version is left alone",
			running: serverIdentity{Version: "v1.2.3", Pid: 4242},
			want:    "v1.2.3",
			action:  keepServer,
		},
		{
			name:    "older version with a proven pid is replaced",
			running: serverIdentity{Version: "v1.2.2", Pid: 4242},
			want:    "v1.2.3",
			action:  replaceServer,
		},
		{
			name:    "newer version is also replaced",
			running: serverIdentity{Version: "v2.0.0", Pid: 4242},
			want:    "v1.2.3",
			action:  replaceServer,
		},
		{
			// The transition case. Replacing it would mean signalling a PID
			// read from a file rather than one proven over the wire, which is
			// the exact defect the PID binding closed.
			name:    "stale but unproven pid is reported, never signalled",
			running: serverIdentity{Version: "", Pid: 0},
			want:    "v1.2.3",
			action:  reportStale,
		},
		{
			name:    "unproven pid on a matching version is still left alone",
			running: serverIdentity{Version: "v1.2.3", Pid: 0},
			want:    "v1.2.3",
			action:  keepServer,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := decideStale(tc.running, tc.want); got != tc.action {
				t.Errorf("decideStale(%+v, %q) = %v, want %v", tc.running, tc.want, got, tc.action)
			}
		})
	}
}

// stopStale's contract is "the address is free when this returns nil", and the
// caller spawns a replacement on the strength of it. A failed signal does not
// establish that — the process may still hold the listener — so the condition
// has to be observed, never inferred from the signal.
func TestStopStale_ReportsOnlyAnObservedFreeAddress(t *testing.T) {
	// A PID the OS refuses to signal, so only the address decides the outcome.
	const unsignallable = -1

	t.Run("listener still answering is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}))
		t.Cleanup(srv.Close)

		if err := stopStale(unsignallable, srv.URL+"/healthz", 20*time.Millisecond, 200*time.Millisecond); err == nil {
			t.Fatal("stopStale returned nil while the listener was still answering: " +
				"the caller would now spawn a replacement that cannot bind")
		}
	})

	t.Run("address already free returns promptly", func(t *testing.T) {
		if err := stopStale(unsignallable, "http://127.0.0.1:1/healthz", 20*time.Millisecond, 2*time.Second); err != nil {
			t.Fatalf("stopStale = %v, want nil when the address is already free", err)
		}
	})
}
