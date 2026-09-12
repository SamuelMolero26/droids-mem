package main

import (
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/spf13/cobra"

	"github.com/samuelmolero26/droids-mem/internal/mcpserver"
	"github.com/samuelmolero26/droids-mem/internal/state"
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

A healthy listener must also pass an /identity challenge proving it holds the
same bearer token; an unknown process squatting the port is reported as an
error instead of "already_running".

Idempotent: safe to call before every client request.

Bearer token resolution: DROIDS_MEM_MCP_TOKEN env → ~/.droids-mem/token file →
auto-generated tok_<ULID> persisted 0600. First-run is zero-config.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			effectiveAddr := envOr("DROIDS_MEM_MCP_ADDR", addr)
			base := baseURL(effectiveAddr)
			healthURL := base + "/healthz"

			tok, err := state.LoadOrCreateToken()
			if err != nil {
				return fmt.Errorf("load token: %w", err)
			}

			replaced := 0
			if ping(healthURL, 200*time.Millisecond) {
				// Anti port-squatting: never report "already_running" (and let
				// clients send the bearer token) until whatever answers on this
				// port proves it holds the same token.
				id, err := verifyServer(base, tok, 500*time.Millisecond)
				if err != nil {
					return fmt.Errorf("a process answers /healthz on %s but failed the token identity check — refusing to treat it as droids-mem serve (another process may be squatting the port): %w", effectiveAddr, err)
				}
				switch decideStale(id, version) {
				case keepServer:
					fmt.Fprintln(os.Stdout, `{"status":"already_running"}`)
					return nil
				case reportStale:
					fmt.Fprintf(os.Stderr, "warning: the daemon on %s runs superseded code, but predates PID binding and cannot prove which process it is, so it is left running. Stop it by hand to pick up this build (%s).\n", effectiveAddr, version)
					fmt.Fprintln(os.Stdout, `{"status":"already_running","stale":true}`)
					return nil
				case replaceServer:
					if err := stopStale(id.Pid, healthURL, probe, timeout); err != nil {
						return err
					}
					replaced = id.Pid
				}
			}

			pid, err := spawnDetached(effectiveAddr, tok)
			if err != nil {
				return fmt.Errorf("spawn serve: %w", err)
			}

			deadline := time.Now().Add(timeout)
			for time.Now().Before(deadline) {
				if ping(healthURL, probe) {
					if _, err := verifyServer(base, tok, 500*time.Millisecond); err != nil {
						return fmt.Errorf("spawned server (pid=%d) failed identity check: %w", pid, err)
					}
					if replaced != 0 {
						fmt.Fprintf(os.Stdout, "{\"status\":\"restarted\",\"pid\":%d,\"replaced\":%d}\n", pid, replaced)
					} else {
						fmt.Fprintf(os.Stdout, "{\"status\":\"started\",\"pid\":%d}\n", pid)
					}
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

// baseURL builds the loopback base URL from a bind address.
// "0.0.0.0:7777" / ":7777" → "http://localhost:7777".
func baseURL(bindAddr string) string {
	host, port, err := net.SplitHostPort(bindAddr)
	if err != nil {
		// bindAddr had no port (e.g. "localhost") — assume the default port.
		host, port = bindAddr, "7777"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port)
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

// serverIdentity is what a verified /identity answer established about the
// listener. Pid is 0 when the server did not prove one: it predates PID
// binding, so it established the token and nothing about which process it is.
type serverIdentity struct {
	Version string
	Pid     int
}

// verifyServer challenges the process behind base with a fresh nonce and
// checks that its /identity proof equals HMAC-SHA256(token, nonce). Only a
// holder of the same bearer token can answer correctly, so success means the
// listener really is droids-mem serve (or an equivalent token holder) — not
// an arbitrary process that grabbed the port to harvest tokens.
//
// A returned Pid is proven, not merely reported: it is bound into a second
// HMAC, so a process relaying another server's answers cannot substitute a PID
// of its choosing. Callers that signal must require it; callers that only ask
// "is a server up" must ignore it, or a healthy older daemon reads as hostile.
func verifyServer(base, token string, timeout time.Duration) (serverIdentity, error) {
	nonce := ulid.Make().String()
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(base + "/identity?nonce=" + url.QueryEscape(nonce))
	if err != nil {
		return serverIdentity{}, fmt.Errorf("identity probe: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return serverIdentity{}, fmt.Errorf("identity probe: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return serverIdentity{}, fmt.Errorf("identity probe: read body: %w", err)
	}
	var payload struct {
		Server   string `json:"server"`
		Proof    string `json:"proof"`
		Version  string `json:"version"`
		Pid      int    `json:"pid"`
		PidProof string `json:"pid_proof"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return serverIdentity{}, fmt.Errorf("identity probe: parse body: %w", err)
	}
	want := mcpserver.IdentityProof(token, nonce)
	if !hmac.Equal([]byte(payload.Proof), []byte(want)) {
		return serverIdentity{}, fmt.Errorf("identity proof mismatch (server=%q)", payload.Server)
	}
	id := serverIdentity{Version: payload.Version}
	if payload.PidProof != "" {
		wantPid := mcpserver.IdentityPidProof(token, nonce, payload.Pid)
		if !hmac.Equal([]byte(payload.PidProof), []byte(wantPid)) {
			return serverIdentity{}, fmt.Errorf("identity pid proof mismatch (server=%q, pid=%d)", payload.Server, payload.Pid)
		}
		id.Pid = payload.Pid
	}
	return id, nil
}

// staleAction is what to do about a daemon that already answers on the
// address and has proven it holds the token.
type staleAction int

const (
	// keepServer: the daemon runs this build. Leave it alone.
	keepServer staleAction = iota
	// replaceServer: it runs other code and proved which process it is.
	replaceServer
	// reportStale: it runs other code but proved no PID, so there is nothing
	// safe to signal. Tell the user; change nothing.
	reportStale
)

// decideStale compares the daemon's reported version against this binary's.
//
// Nothing else restarts the daemon — `upgrade` replaces the executable on disk
// and returns — so without this a daemon keeps serving superseded code until
// the machine reboots. Any difference counts, in either direction: the daemon
// should be running the build the caller actually has, and a downgrade leaves
// code the caller did not build just as surely as an upgrade does.
//
// A daemon that proves no PID is never signalled. It predates PID binding, so
// acting on it would mean signalling a PID read from a file rather than one
// proven over the wire — the exact defect that binding closed. It is reported
// instead, which is a bounded, one-time cost: from the release that carries
// this onward, every daemon can prove a PID.
func decideStale(running serverIdentity, want string) staleAction {
	if running.Version == want {
		return keepServer
	}
	if running.Pid == 0 {
		return reportStale
	}
	return replaceServer
}

// stopStale SIGTERMs a daemon that proved it is pid, and waits only until it
// releases the listener — not for its drain to finish. http.Server.Shutdown
// closes listeners before waiting on in-flight connections, so the address is
// free within milliseconds even when a live MCP stream holds the drain open
// for the full shutdown grace. The replacement binds while the outgoing
// process is still winding down.
func stopStale(pid int, healthURL string, probe, timeout time.Duration) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find stale server (pid=%d): %w", pid, err)
	}
	// A failed signal is not reported: the process may simply have exited
	// between the probe and here, which is the desired end state. But it is
	// not *assumed* to mean that either — a signal can fail while the process
	// is alive and still holding the listener, and a replacement spawned on
	// that assumption cannot bind. Only the wait below settles it, by
	// observing the address rather than inferring from the signal.
	_ = proc.Signal(syscall.SIGTERM)

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !ping(healthURL, probe) {
			return nil
		}
		time.Sleep(probe)
	}
	return fmt.Errorf("stale server (pid=%d) still holds %s after %s", pid, healthURL, timeout)
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
	// #nosec G304 -- logPath derives from the trusted state dir (~/.droids-mem),
	// not from user input.
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, fmt.Errorf("open log: %w", err)
	}
	defer logFile.Close()
	// log-append fd; close error = missing log line at worst; child has its own fd copy

	// #nosec G204 -- re-exec of our own binary (os.Executable), fixed argv.
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
	if err := os.WriteFile(pidPath, fmt.Appendf(nil, "%d\n", pid), 0o600); err != nil {
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
