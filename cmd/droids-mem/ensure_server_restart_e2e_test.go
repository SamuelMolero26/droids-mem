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

// A daemon running superseded code is the normal state after an upgrade:
// nothing restarts it, so it serves the old build until the box reboots.
// ensure-server replaces it once it can prove which process it is.
func TestE2E_EnsureServerReplacesStaleDaemon(t *testing.T) {
	dir := t.TempDir()
	oldBin := buildVersioned(t, dir, "v1.0.0")
	newBin := buildVersioned(t, dir, "v2.0.0")
	env := newDaemonEnv(t)

	started := env.run(t, oldBin, "ensure-server", "--addr", env.addr)
	if started["status"] != "started" {
		t.Fatalf("first ensure-server = %v, want started", started)
	}
	oldPid := env.pidfile(t)

	got := env.run(t, newBin, "ensure-server", "--addr", env.addr)
	if got["status"] != "restarted" {
		t.Fatalf("ensure-server = %v, want restarted (daemon runs v1.0.0, binary is v2.0.0)", got)
	}
	if replaced, ok := got["replaced"].(float64); !ok || int(replaced) != oldPid {
		t.Errorf("replaced = %v, want the stale pid %d", got["replaced"], oldPid)
	}

	newPid := env.pidfile(t)
	if newPid == oldPid {
		t.Fatalf("pidfile still names %d: the stale daemon was not replaced", oldPid)
	}
	// The outgoing process drains in the background; it must actually be gone.
	deadline := time.Now().Add(15 * time.Second)
	for alive(oldPid) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if alive(oldPid) {
		t.Errorf("stale daemon %d still running after replacement", oldPid)
	}
	if !alive(newPid) {
		t.Errorf("replacement daemon %d is not running", newPid)
	}
}

// The other half of the contract: a daemon already on this build must not be
// disturbed. Restarting it would sever live MCP streams for no reason, on
// every single ensure-server call.
func TestE2E_EnsureServerKeepsCurrentDaemon(t *testing.T) {
	dir := t.TempDir()
	bin := buildVersioned(t, dir, "v1.0.0")
	env := newDaemonEnv(t)

	env.run(t, bin, "ensure-server", "--addr", env.addr)
	pid := env.pidfile(t)

	got := env.run(t, bin, "ensure-server", "--addr", env.addr)
	if got["status"] != "already_running" {
		t.Fatalf("ensure-server = %v, want already_running", got)
	}
	if got["stale"] != nil {
		t.Errorf("stale = %v, want absent on a current daemon", got["stale"])
	}
	if env.pidfile(t) != pid {
		t.Errorf("pidfile changed from %d: a current daemon was needlessly replaced", pid)
	}
	if !alive(pid) {
		t.Errorf("daemon %d was killed", pid)
	}
}
