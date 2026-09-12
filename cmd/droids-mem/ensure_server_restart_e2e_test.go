package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// buildVersioned builds the binary with the release version the workflow
// injects, so the daemon reports a version the way a real release does.
func buildVersioned(t *testing.T, dir, version string) string {
	t.Helper()
	path := filepath.Join(dir, "droids-mem-"+version)
	out, err := exec.Command("go", "build",
		"-ldflags", "-X main.version="+version, "-o", path, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("build %s: %v\n%s", version, err, out)
	}
	return path
}

type daemonEnv struct {
	home string
	addr string
}

func (e daemonEnv) run(t *testing.T, bin string, args ...string) map[string]any {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(),
		"DROIDS_MEM_HOME="+e.home,
		"DROIDS_MEM_DB="+filepath.Join(e.home, "mem.db"),
		"DROIDS_MEM_MCP_ADDR="+e.addr,
	)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %v: %v\nstdout: %s", filepath.Base(bin), args, err, out)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("parse %q: %v", out, err)
	}
	return got
}

func (e daemonEnv) pidfile(t *testing.T) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.home, "mcp.pid"))
	if err != nil {
		t.Fatalf("read pidfile: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("parse pidfile %q: %v", b, err)
	}
	return pid
}

func newDaemonEnv(t *testing.T) daemonEnv {
	t.Helper()
	e := daemonEnv{home: t.TempDir(), addr: freePort(t)}
	t.Cleanup(func() {
		b, err := os.ReadFile(filepath.Join(e.home, "mcp.pid"))
		if err != nil {
			return
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			if p, err := os.FindProcess(pid); err == nil {
				_ = p.Signal(syscall.SIGKILL)
			}
		}
	})
	return e
}

func alive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// A daemon running superseded code is the normal state after an upgrade —
// nothing restarts it, so it keeps serving the old build until the box reboots.
// ensure-server replaces one whose version differs from its own, and must leave
// one that matches alone: restarting that would sever live MCP streams on every
// single call.
func TestE2E_EnsureServerReplacesOnlyAStaleDaemon(t *testing.T) {
	tests := []struct {
		name       string
		running    string // version of the daemon already up
		calling    string // version of the binary running ensure-server
		wantStatus string
		wantSwap   bool
	}{
		{
			name:       "daemon on another version is replaced",
			running:    "v1.0.0",
			calling:    "v2.0.0",
			wantStatus: "restarted",
			wantSwap:   true,
		},
		{
			name:       "daemon on this version is left alone",
			running:    "v1.0.0",
			calling:    "v1.0.0",
			wantStatus: "already_running",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			env := newDaemonEnv(t)

			if got := env.run(t, buildVersioned(t, dir, tc.running), "ensure-server", "--addr", env.addr); got["status"] != "started" {
				t.Fatalf("first ensure-server = %v, want started", got)
			}
			before := env.pidfile(t)

			got := env.run(t, buildVersioned(t, dir, tc.calling), "ensure-server", "--addr", env.addr)
			if got["status"] != tc.wantStatus {
				t.Fatalf("ensure-server = %v, want %q", got, tc.wantStatus)
			}
			after := env.pidfile(t)
			if swapped := after != before; swapped != tc.wantSwap {
				t.Fatalf("pid changed = %v, want %v (was %d, now %d)", swapped, tc.wantSwap, before, after)
			}
			if !alive(after) {
				t.Errorf("serving daemon %d is not running", after)
			}
			if !tc.wantSwap {
				return
			}

			if replaced, ok := got["replaced"].(float64); !ok || int(replaced) != before {
				t.Errorf("replaced = %v, want the stale pid %d", got["replaced"], before)
			}
			// The outgoing process drains in the background, so give it room.
			deadline := time.Now().Add(15 * time.Second)
			for alive(before) && time.Now().Before(deadline) {
				time.Sleep(50 * time.Millisecond)
			}
			if alive(before) {
				t.Errorf("stale daemon %d still running after replacement", before)
			}
		})
	}
}
