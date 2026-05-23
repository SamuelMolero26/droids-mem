package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/samuelmolero/droids-mem/internal/mcpserver"
	"github.com/samuelmolero/droids-mem/internal/state"
)

func newEnsureServerCmd() *cobra.Command {
	var (
		addr    string
		timeout time.Duration
		probe   time.Duration
	)
	cmd := &cobra.Command{
		Use:   "ensure-server",
		Short: "Start the MCP bridge if it is not already running",
		Long: `ensure-server pings /healthz. If the server is up it returns immediately.
Otherwise it spawns "droids-mem serve" as a detached background process,
polls /healthz until ready (default 5s), and exits 0.

Idempotent: safe to call before every client request.

Bearer token resolution: DROIDS_MEM_MCP_TOKEN env → ~/.droids-mem/token file →
auto-generated tok_<ULID> persisted 0600. First-run is zero-config.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			effectiveAddr := envOr("DROIDS_MEM_MCP_ADDR", addr)
			healthURL := healthzURL(effectiveAddr)

			if ping(healthURL, 200*time.Millisecond) {
				fmt.Fprintln(os.Stdout, `{"status":"already_running"}`)
				return nil
			}

			tok, err := state.LoadOrCreateToken()
			if err != nil {
				return fmt.Errorf("load token: %w", err)
			}

			pid, err := spawnDetached(effectiveAddr, tok)
			if err != nil {
				return fmt.Errorf("spawn serve: %w", err)
			}

			deadline := time.Now().Add(timeout)
			for time.Now().Before(deadline) {
				if ping(healthURL, probe) {
					fmt.Fprintf(os.Stdout, "{\"status\":\"started\",\"pid\":%d}\n", pid)
					return nil
				}
				time.Sleep(probe)
			}
			return fmt.Errorf("server did not become healthy within %s (pid=%d)", timeout, pid)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", mcpserver.DefaultAddr, "address to probe / pass to spawned serve")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Second, "max time to wait for healthz")
	cmd.Flags().DurationVar(&probe, "probe-interval", 100*time.Millisecond, "interval between healthz probes")
	return cmd
}

// healthzURL builds the loopback healthz URL from a bind address.
// "0.0.0.0:7777" / ":7777" → "http://localhost:7777/healthz".
func healthzURL(bindAddr string) string {
	host, port, err := net.SplitHostPort(bindAddr)
	if err != nil {
		return "http://localhost" + bindAddr + "/healthz"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz"
}

func ping(url string, timeout time.Duration) bool {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// spawnDetached re-execs the current binary as `droids-mem serve` in a new
// session so it survives the parent exiting. stdout+stderr go to mcp.log
// in the state dir. The child's PID is written to mcp.pid.
func spawnDetached(addr, token string) (int, error) {
	self, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("resolve self path: %w", err)
	}

	dir, err := state.Dir()
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, fmt.Errorf("create state dir: %w", err)
	}

	logPath := filepath.Join(dir, state.LogFile)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, fmt.Errorf("open log: %w", err)
	}
	defer logFile.Close()

	cmd := exec.Command(self, "serve")
	// Forward effective addr + token to child via env. Child's LoadOrCreateToken
	// will short-circuit on the env var so parent and child agree even if the
	// on-disk token file race-changes.
	env := append(os.Environ(),
		"DROIDS_MEM_MCP_ADDR="+addr,
		"DROIDS_MEM_MCP_TOKEN="+token,
	)
	cmd.Env = env
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	// Setsid: detach from parent's controlling terminal so the child survives
	// when the caller exits. POSIX-only; ensure-server is not exposed on Windows.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return 0, err
	}

	pid := cmd.Process.Pid
	pidPath := filepath.Join(dir, state.PidFile)
	if err := os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", pid)), 0o600); err != nil {
		// Non-fatal: server still spawned. Surface as log line in mcp.log.
		fmt.Fprintf(logFile, "warn: write pidfile: %v\n", err)
	}
	// Release the Process so we do not block waiting on the child.
	_ = cmd.Process.Release()

	// Best-effort: prefix log entries with start marker.
	fmt.Fprintf(logFile, "--- droids-mem serve spawned pid=%d at %s ---\n",
		pid, time.Now().UTC().Format(time.RFC3339))

	return pid, nil
}

