package main

import "testing"

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
