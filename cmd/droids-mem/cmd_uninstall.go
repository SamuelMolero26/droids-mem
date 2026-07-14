package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/samuelmolero26/droids-mem/internal/state"
)

// sessionHookMarker identifies a droids-mem hook entry by the subcommand it
// runs. We match on this substring, not the full `<self> session hook` string,
// so a moved/reinstalled binary still uninstalls cleanly (ADR-0024 §5).
const sessionHookMarker = "session hook"

// newUninstallCmd is the reverse of install (issue #27): it strips only the
// wiring droids-mem added, leaving each host's file as if untouched. Default
// never deletes the memory corpus — that's the opt-in --purge (ADR-0024 §6).
func newUninstallCmd() *cobra.Command {
	var project, all, purge bool
	var host string
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove droids-mem's wiring from an agent host (reverse of install)",
		Long: "Strip the session-memory hooks from Claude Code's settings.json.\n" +
			"Default target is the user settings (~/.claude/settings.json); use\n" +
			"--project to target ./.claude/settings.json. Removes only entries\n" +
			"droids-mem added; unrelated hooks and settings are preserved.\n\n" +
			"--all reverses the full bootstrap: hooks + `claude mcp remove` + the\n" +
			"CLAUDE.md block + stopping the MCP daemon. Each step is best-effort.\n\n" +
			"--purge additionally deletes the state dir (~/.droids-mem/: mem.db,\n" +
			"token, mcp.pid) — the destructive clean-slate. Implies --all's teardown.\n\n" +
			"--host codex|opencode removes the stdio MCP registration from that\n" +
			"host's config instead. Idempotent; --all/--purge not supported there.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if host != "claude" {
				if all || purge {
					writeError("usage", "--all/--purge are Claude-only; --host "+host+" just removes the stdio MCP registration", false)
					exitWith(ExitUsage)
				}
				return uninstallHost(host)
			}

			path, err := claudeSettingsPath(project)
			if err != nil {
				writeError("uninstall_failed", err.Error(), false)
				exitWith(ExitError)
			}
			removed, err := removeHooksFrom(path)
			if err != nil {
				writeError("uninstall_failed", err.Error(), true)
				exitWith(ExitError)
			}
			result := map[string]any{
				"status":         "uninstalled",
				"settings":       path,
				"events_removed": removed,
			}

			if !all && !purge {
				result["next_step"] = "run `droids-mem uninstall --all` to also remove the MCP registration + CLAUDE.md block (add --purge to delete the memory database)"
				writeJSON(result)
				return nil
			}

			// --all / --purge: best-effort per step, report each outcome.
			result["mcp_registration"] = unregisterClaudeMCPStatus()
			result["claude_md"] = removeClaudeSnippetStatus(project)
			result["server"] = stopServerStatus() // stop before any purge
			if purge {
				result["purge"] = purgeStateStatus()
			}
			writeJSON(result)
			return nil
		},
	}
	cmd.Flags().BoolVar(&project, "project", false, "Uninstall from ./.claude/settings.json instead of the user settings")
	cmd.Flags().BoolVar(&all, "all", false, "Full teardown: hooks + claude mcp remove + CLAUDE.md block + stop daemon")
	cmd.Flags().BoolVar(&purge, "purge", false, "Also delete the state dir (~/.droids-mem/: mem.db, token) — destructive")
	cmd.Flags().StringVar(&host, "host", "claude", "Target host: claude, codex, or opencode")
	return cmd
}

// removeHooksFrom drops every hook entry that runs `session hook` from
// settings.json, pruning event arrays and the hooks object once empty so no
// residue is left. Returns the events touched. A missing file, a file with no
// hooks object, or one holding none of ours is a no-op (left untouched).
func removeHooksFrom(path string) ([]string, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- settings.json location, not user input
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	settings := map[string]any{}
	if err := json.Unmarshal(b, &settings); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return nil, nil
	}

	removed := []string{}
	for _, e := range claudeHookEvents {
		entries, _ := hooks[e.name].([]any)
		if len(entries) == 0 {
			continue
		}
		kept := make([]any, 0, len(entries))
		for _, entry := range entries {
			if entryHasSessionHook(entry) {
				continue
			}
			kept = append(kept, entry)
		}
		if len(kept) == len(entries) {
			continue // nothing of ours in this event
		}
		removed = append(removed, e.name)
		if len(kept) == 0 {
			delete(hooks, e.name)
		} else {
			hooks[e.name] = kept
		}
	}
	if len(removed) == 0 {
		return nil, nil // nothing ours present — don't rewrite the file
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	} else {
		settings["hooks"] = hooks
	}

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal settings: %w", err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	return removed, nil
}

// entryHasSessionHook reports whether a hooks entry runs the session-hook
// command (matched by the sessionHookMarker substring).
func entryHasSessionHook(entry any) bool {
	em, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	inner, ok := em["hooks"].([]any)
	if !ok {
		return false
	}
	for _, h := range inner {
		hm, ok := h.(map[string]any)
		if !ok {
			continue
		}
		if cmd, _ := hm["command"].(string); strings.Contains(cmd, sessionHookMarker) {
			return true
		}
	}
	return false
}

// unregisterClaudeMCPStatus reverses `claude mcp add` at user scope. Only the
// client's own CLI can edit its registry, so we drive it.
func unregisterClaudeMCPStatus() string {
	claude, err := exec.LookPath("claude")
	if err != nil {
		return "manual: claude CLI not in PATH — run `claude mcp remove droids-mem`"
	}
	// #nosec G204 -- claude path from exec.LookPath, fixed argv.
	if exec.Command(claude, "mcp", "get", "droids-mem").Run() != nil {
		return "already_absent"
	}
	// #nosec G204 -- claude path from exec.LookPath, fixed argv.
	if out, err := exec.Command(claude, "mcp", "remove", "droids-mem").CombinedOutput(); err != nil {
		return "error: " + strings.TrimSpace(string(out))
	}
	return "removed"
}

// removeClaudeSnippetStatus splices the exact embedded block out of CLAUDE.md.
// The block carries no binary path, so an exact-string match is safe. A marker
// present but no exact match means the user edited the block — we refuse to
// guess its bounds and report manual_removal_needed (ADR-0024 §5).
func removeClaudeSnippetStatus(project bool) string {
	path, err := claudeMdPath(project)
	if err != nil {
		return "error: " + err.Error()
	}
	b, err := os.ReadFile(path) // #nosec G304 -- fixed CLAUDE.md location, not user input
	if err != nil {
		if os.IsNotExist(err) {
			return "already_absent"
		}
		return "error: " + err.Error()
	}
	s := string(b)
	if !strings.Contains(s, claudeSnippetMarker) {
		return "already_absent"
	}
	// Install prepends a newline when the file was non-empty; strip that form
	// first, then the bare block (file install created from scratch).
	next := strings.Replace(s, "\n"+claudeSnippet, "", 1)
	if next == s {
		next = strings.Replace(s, claudeSnippet, "", 1)
	}
	if next == s {
		return "manual_removal_needed: " + path
	}
	// #nosec G703 -- fixed CLAUDE.md location, not user input
	if err := os.WriteFile(path, []byte(next), 0o600); err != nil {
		return "error: " + err.Error()
	}
	return "removed: " + path
}

// stopServerStatus SIGTERMs the daemon recorded in mcp.pid (server does a
// graceful Shutdown) and clears the pidfile. The symmetric counterpart of
// ensure-server's spawn.
func stopServerStatus() string {
	dir, err := state.Dir()
	if err != nil {
		return "error: " + err.Error()
	}
	pidPath := filepath.Join(dir, state.PidFile)
	b, err := os.ReadFile(pidPath) // #nosec G304 -- fixed pidfile path
	if err != nil {
		if os.IsNotExist(err) {
			return "not_running"
		}
		return "error: " + err.Error()
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return "error: bad pidfile: " + err.Error()
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return "error: " + err.Error()
	}
	// ponytail: SIGTERM is POSIX-only; droids-mem targets macOS/Linux.
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		_ = os.Remove(pidPath) // process already gone
		return "not_running"
	}
	_ = os.Remove(pidPath)
	return "stopped"
}

// purgeStateStatus deletes the whole state dir. Destructive and opt-in only.
func purgeStateStatus() string {
	dir, err := state.Dir()
	if err != nil {
		return "error: " + err.Error()
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return "already_absent"
	}
	if err := os.RemoveAll(dir); err != nil {
		return "error: " + err.Error()
	}
	return "purged: " + dir
}

// uninstallHost removes the stdio MCP registration from a non-Claude host.
func uninstallHost(host string) error {
	switch host {
	case "codex":
		return uninstallCodex()
	case "opencode":
		return uninstallOpencode()
	default:
		writeError("usage", "unknown --host "+host+" (want claude, codex, or opencode)", false)
		exitWith(ExitUsage)
		return nil
	}
}

// uninstallCodex removes the [mcp_servers.droids-mem] table from config.toml
// by a marker→boundary scan (the table embeds the binary path, so an exact
// match would miss after a binary move; ADR-0024 §5).
func uninstallCodex() error {
	home, err := os.UserHomeDir()
	if err != nil {
		writeError("uninstall_failed", "resolve home dir: "+err.Error(), false)
		exitWith(ExitError)
	}
	path := filepath.Join(home, ".codex", "config.toml")
	b, err := os.ReadFile(path) // #nosec G304 -- fixed config location, not user input
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(map[string]any{"status": "already_absent", "host": "codex", "config": path})
			return nil
		}
		writeError("uninstall_failed", "read "+path+": "+err.Error(), true)
		exitWith(ExitError)
	}
	next, removed := stripTOMLTable(string(b), codexMCPMarker)
	if !removed {
		writeJSON(map[string]any{"status": "already_absent", "host": "codex", "config": path})
		return nil
	}
	// #nosec G703 -- path is a fixed config location, not user input
	if err := os.WriteFile(path, []byte(next), 0o600); err != nil {
		writeError("uninstall_failed", "write "+path+": "+err.Error(), true)
		exitWith(ExitError)
	}
	writeJSON(map[string]any{"status": "uninstalled", "host": "codex", "config": path})
	return nil
}

// stripTOMLTable removes the table headed by marker (a full-line `[table]`)
// through the line before the next top-level `[` header or EOF. Line-based,
// no TOML dependency — top-level tables are order-independent so this is safe.
func stripTOMLTable(content, marker string) (string, bool) {
	lines := strings.Split(content, "\n")
	start := -1
	for i, ln := range lines {
		if strings.TrimSpace(ln) == marker {
			start = i
			break
		}
	}
	if start == -1 {
		return content, false
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
			end = i
			break
		}
	}
	kept := append(lines[:start:start], lines[end:]...)
	return strings.Join(kept, "\n"), true
}

// uninstallOpencode deletes the mcp["droids-mem"] key from opencode.json,
// pruning an emptied mcp object. Same read-mutate-write as install.
func uninstallOpencode() error {
	home, err := os.UserHomeDir()
	if err != nil {
		writeError("uninstall_failed", "resolve home dir: "+err.Error(), false)
		exitWith(ExitError)
	}
	path := filepath.Join(home, ".config", "opencode", "opencode.json")
	b, err := os.ReadFile(path) // #nosec G304 -- fixed config location, not user input
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(map[string]any{"status": "already_absent", "host": "opencode", "config": path})
			return nil
		}
		writeError("uninstall_failed", "read "+path+": "+err.Error(), true)
		exitWith(ExitError)
	}
	config := map[string]any{}
	if err := json.Unmarshal(b, &config); err != nil {
		writeError("uninstall_failed", "parse "+path+": "+err.Error(), false)
		exitWith(ExitError)
	}
	mcp, _ := config["mcp"].(map[string]any)
	if _, ok := mcp["droids-mem"]; !ok {
		writeJSON(map[string]any{"status": "already_absent", "host": "opencode", "config": path})
		return nil
	}
	delete(mcp, "droids-mem")
	if len(mcp) == 0 {
		delete(config, "mcp")
	} else {
		config["mcp"] = mcp
	}
	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		writeError("uninstall_failed", "marshal config: "+err.Error(), false)
		exitWith(ExitError)
	}
	// #nosec G703 -- path is a fixed config location, not user input
	if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
		writeError("uninstall_failed", "write "+path+": "+err.Error(), true)
		exitWith(ExitError)
	}
	writeJSON(map[string]any{"status": "uninstalled", "host": "opencode", "config": path})
	return nil
}

// claudeMdPath resolves the CLAUDE.md target: ./CLAUDE.md with --project, else
// ~/.claude/CLAUDE.md. Shared by install (append) and uninstall (splice).
func claudeMdPath(project bool) (string, error) {
	if project {
		return "CLAUDE.md", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".claude", "CLAUDE.md"), nil
}
