package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

// claudeSnippet is the CLAUDE.md compose-guidance block (the model-judgment
// half of the intake gate, ADR-0016). Embedded so `install --all` can append
// it without needing the repo checkout. This file is the single source of the
// block; hooks/session-memory.md points here rather than duplicating it.
//
//go:embed claude_snippet.md
var claudeSnippet string

//go:embed graph_snippet.md
var graphSnippet string

// graphSnippetMarker detects a prior graph-block append (idempotency).
const graphSnippetMarker = "## droids-mem code graph"

// claudeSnippetMarker detects a prior append (idempotency).
const claudeSnippetMarker = "## droids-mem session memory"

// hookEvent maps a Claude Code hook event to an optional tool matcher. A matcher
// limits how often the hook fires (fewer binary spawns); event-less hooks fire
// every time.
type hookEvent struct {
	name    string
	matcher string
}

var claudeHookEvents = []hookEvent{
	{"PostToolUse", "Edit|Write|MultiEdit|NotebookEdit"},
	{"Stop", ""},
	{"SessionEnd", ""},
	{"SessionStart", ""},
	{"UserPromptSubmit", ""},
}

// newInstallCmd wires droids-mem session memory into Claude Code in one shot:
// it merges the hook entries into settings.json, idempotently, pointing every
// event at `<this binary> session hook`. No shell scripts, no jq.
func newInstallCmd() *cobra.Command {
	var project, printOnly, all bool
	var host string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Wire droids-mem into an agent host (Claude Code, codex, opencode)",
		Long: "Merge the session-memory hooks into Claude Code's settings.json.\n" +
			"Default target is the user settings (~/.claude/settings.json); use\n" +
			"--project to target ./.claude/settings.json instead. Idempotent and\n" +
			"non-destructive — existing settings and hooks are preserved.\n\n" +
			"--all performs the full bootstrap in one shot: hooks + register the\n" +
			"server with the Claude Code CLI (user scope, stdio transport: Claude\n" +
			"spawns it per session, so there is no daemon and no token) + append\n" +
			"the compose-guidance block to CLAUDE.md, then register every detected\n" +
			"codex/opencode install too. Each step is idempotent.\n\n" +
			"--host codex|opencode registers droids-mem as a stdio MCP server in\n" +
			"that host's config instead (codex: ~/.codex/config.toml; opencode:\n" +
			"~/.config/opencode/opencode.json) and, with --project, appends the\n" +
			"code-graph guidance to ./AGENTS.md. Idempotent; --all not supported.\n\n" +
			"--all also appends the code-graph guidance to CLAUDE.md.",
		RunE: func(cmd *cobra.Command, args []string) error {
			self, err := os.Executable()
			if err != nil {
				writeError("install_failed", "cannot resolve binary path: "+err.Error(), false)
				exitWith(ExitError)
			}
			if host != "claude" {
				if all {
					writeError("usage", "--all is Claude-only; --host "+host+" just registers the stdio MCP server", false)
					exitWith(ExitUsage)
				}
				return installHost(host, self, printOnly, project)
			}
			hookCmd := self + " session hook"

			if printOnly {
				writeJSON(buildHooksBlock(hookCmd)) // {"hooks":{...}}
				return nil
			}

			settingsPath, err := claudeSettingsPath(project)
			if err != nil {
				writeError("install_failed", err.Error(), false)
				exitWith(ExitError)
			}
			added, err := mergeHooksInto(settingsPath, hookCmd)
			if err != nil {
				writeError("install_failed", err.Error(), true)
				exitWith(ExitError)
			}
			result := map[string]any{
				"status":       "installed",
				"settings":     settingsPath,
				"events_added": added,
				"command":      hookCmd,
			}

			if !all {
				result["next_step"] = "run `droids-mem install --all` for the full bootstrap (appends the embedded CLAUDE.md snippet; no repo checkout needed)"
				writeJSON(result)
				return nil
			}

			// --all: best-effort per step — report each outcome instead of
			// aborting the whole bootstrap on the first failure.
			result["mcp_registration"] = stepStatus(registerClaudeMCP(self))
			mdPath, appended, err := appendClaudeSnippet(project)
			switch {
			case err != nil:
				result["claude_md"] = "error: " + err.Error()
			case appended:
				result["claude_md"] = "appended: " + mdPath
			default:
				result["claude_md"] = "already_present: " + mdPath
			}
			// The block tells the agent to call graph tools; without a
			// registered server they do not exist.
			claudeOK := err == nil && result["mcp_registration"] == "ok"
			if claudeOK {
				result["graph_block"] = graphBlockStatus(mdPath)
			} else if err == nil {
				result["graph_block"] = "skipped: MCP server not registered"
			}
			hostsOK := installOtherHosts(self, result)
			if project {
				// One AGENTS.md block and one index for the whole run, however
				// many hosts registered.
				if hostsOK {
					result["agents_md"] = graphBlockStatus("AGENTS.md")
				}
				if claudeOK || hostsOK {
					result["graph_index"] = startGraphIndex(self)
				}
			}
			writeJSON(result)
			return nil
		},
	}
	cmd.Flags().BoolVar(&project, "project", false, "Install into ./.claude/settings.json instead of the user settings")
	cmd.Flags().BoolVar(&printOnly, "print", false, "Print the hooks block + MCP config instead of writing files")
	cmd.Flags().BoolVar(&all, "all", false, "Full bootstrap: hooks + claude mcp add (stdio) + CLAUDE.md snippet + detected codex/opencode")
	cmd.Flags().StringVar(&host, "host", "claude", "Target host: claude, codex, or opencode")
	return cmd
}

// codexMCPBlock renders the config.toml table registering the stdio bridge.
func codexMCPBlock(self string) string {
	return fmt.Sprintf("[mcp_servers.droids-mem]\ncommand = %q\nargs = [\"serve\", \"--stdio\"]\n", self)
}

// codexMCPMarker detects a prior install (idempotency).
const codexMCPMarker = "[mcp_servers.droids-mem]"

// stepError marks whether a failed host step is worth retrying.
type stepError struct {
	err       error
	retryable bool
}

func (e *stepError) Error() string { return e.err.Error() }
func (e *stepError) Unwrap() error { return e.err }

func stepErr(retryable bool, format string, a ...any) error {
	return &stepError{fmt.Errorf(format, a...), retryable}
}

func isRetryable(err error) bool {
	se, ok := errors.AsType[*stepError](err)
	return ok && se.retryable
}

// installHost registers droids-mem as a stdio MCP server in a non-Claude
// host's config (ADR-0019). Per-host difference is data — a config snippet +
// a target path — not logic; the stdio instructions string carries the
// self-save summary protocol, so no hook wiring is required for parity.
func installHost(host, self string, printOnly, project bool) error {
	var res map[string]any
	var err error
	switch host {
	case "codex":
		if printOnly {
			fmt.Println(codexMCPBlock(self))
			return nil
		}
		res, err = installCodex(self)
	case "opencode":
		if printOnly {
			writeJSON(map[string]any{"mcp": map[string]any{"droids-mem": opencodeEntry(self)}})
			return nil
		}
		res, err = installOpencode(self)
	default:
		writeError("usage", "unknown --host "+host+" (want claude, codex, or opencode)", false)
		exitWith(ExitUsage)
		return nil
	}
	if err != nil {
		writeError("install_failed", err.Error(), isRetryable(err))
		exitWith(ExitError)
	}
	// Only after the host config is written, so a failed install leaves no
	// AGENTS.md block behind.
	if project {
		res["agents_md"] = graphBlockStatus("AGENTS.md")
		res["graph_index"] = startGraphIndex(self)
	}
	writeJSON(res)
	return nil
}

// installCodex appends the [mcp_servers.droids-mem] table to
// ~/.codex/config.toml. Append-if-absent keeps us dependency-free: TOML
// tables are order-independent at top level, and the marker check makes
// re-runs no-ops. We never rewrite the user's existing config.
func installCodex(self string) (map[string]any, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, stepErr(false, "resolve home dir: %w", err)
	}
	path := filepath.Join(home, ".codex", "config.toml")
	res := map[string]any{"host": "codex", "config": path}
	existing, err := os.ReadFile(path) // #nosec G304 -- fixed config location, not user input
	if err != nil && !os.IsNotExist(err) {
		return nil, stepErr(true, "read %s: %w", path, err)
	}
	if strings.Contains(string(existing), codexMCPMarker) {
		res["status"] = "already_installed"
		return res, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, stepErr(true, "create dir: %w", err)
	}
	out := codexMCPBlock(self)
	if n := len(existing); n > 0 {
		sep := "\n"
		if existing[n-1] != '\n' {
			sep = "\n\n"
		}
		out = string(existing) + sep + out
	}

	// #nosec G703 -- path is a fixed config location, not user input
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		return nil, stepErr(true, "write %s: %w", path, err)
	}
	res["status"] = "installed"
	return res, nil
}

func opencodeEntry(self string) map[string]any {
	return map[string]any{
		"type":    "local",
		"command": []any{self, "serve", "--stdio"},
		"enabled": true,
	}
}

// installOpencode merges the droids-mem stdio server into opencode's global
// config (~/.config/opencode/opencode.json) under the "mcp" key. Same
// read-merge-write pattern as the Claude settings.json merge.
func installOpencode(self string) (map[string]any, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, stepErr(false, "resolve home dir: %w", err)
	}
	path := filepath.Join(home, ".config", "opencode", "opencode.json")
	res := map[string]any{"host": "opencode", "config": path}
	config := map[string]any{}
	if b, err := os.ReadFile(path); err == nil { // #nosec G304 -- fixed config location, not user input
		if err := json.Unmarshal(b, &config); err != nil {
			return nil, stepErr(false, "parse %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, stepErr(true, "read %s: %w", path, err)
	}
	mcp, ok := config["mcp"].(map[string]any)
	if !ok {
		if _, present := config["mcp"]; present {
			// Don't clobber a non-object "mcp" — that's the user's data.
			return nil, stepErr(false, `existing "mcp" key in %s is not an object; refusing to overwrite`, path)
		}
		mcp = map[string]any{}
	}
	if _, ok := mcp["droids-mem"]; ok {
		res["status"] = "already_installed"
		return res, nil
	}
	mcp["droids-mem"] = opencodeEntry(self)
	config["mcp"] = mcp
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, stepErr(true, "create dir: %w", err)
	}
	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, stepErr(false, "marshal config: %w", err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
		return nil, stepErr(true, "write %s: %w", path, err)
	}
	res["status"] = "installed"
	return res, nil
}

// stepStatus renders a bootstrap step outcome for the result JSON.
func stepStatus(err error) string {
	if err != nil {
		return "error: " + err.Error()
	}
	return "ok"
}

// registerClaudeMCP registers droids-mem with the Claude Code CLI at user
// scope (available in every project) over the STDIO transport: Claude spawns
// `<self> serve --stdio` as a child and owns its lifecycle, so there is no
// port to find, no daemon to keep alive and no bearer token to pass.
// Registration is the one bootstrap step MCP itself cannot do — a server
// cannot add itself to a client's config — so we drive the client's own CLI.
func registerClaudeMCP(self string) error {
	claude, err := exec.LookPath("claude")
	if err != nil {
		return errors.New("claude CLI not found in PATH — register manually: claude mcp add --scope user droids-mem -- " + self + " serve --stdio")
	}
	// Remove first, unconditionally. `mcp get` succeeds for an existing HTTP
	// registration too, so an existence check would leave anyone upgrading from
	// the daemon pinned to the old transport for ever. A remove with nothing
	// registered fails harmlessly, which is why its error is ignored.
	// #nosec G204 -- claude path from exec.LookPath, fixed argv.
	_ = exec.Command(claude, "mcp", "remove", "--scope", "user", "droids-mem").Run()

	// #nosec G204 -- claude from exec.LookPath; self from os.Executable.
	out, err := exec.Command(claude, "mcp", "add",
		"--scope", "user", "droids-mem", "--", self, "serve", "--stdio",
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("claude mcp add: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// appendClaudeSnippet appends the embedded compose-guidance block to CLAUDE.md
// (~/.claude/CLAUDE.md, or ./CLAUDE.md with --project). Idempotent: a file
// already containing the snippet heading is left untouched.
func appendClaudeSnippet(project bool) (path string, appended bool, err error) {
	path, err = claudeMdPath(project)
	if err != nil {
		return "", false, err
	}
	appended, err = appendBlock(path, claudeSnippet, claudeSnippetMarker)
	return path, appended, err
}

// graphBlockStatus appends the graph block and renders the outcome as a
// best-effort status string, like the other install steps.
func graphBlockStatus(path string) string {
	switch appended, err := appendGraphBlock(path); {
	case err != nil:
		return "error: " + err.Error()
	case appended:
		return "appended: " + path
	default:
		return "already_present: " + path
	}
}

// otherHosts are the non-Claude hosts --all fans out to; the config dir's
// presence under $HOME is what marks a host as installed.
var otherHosts = []struct {
	name      string
	dir       []string
	install   func(self string) (map[string]any, error)
	uninstall func() (map[string]any, error)
}{
	{"codex", []string{".codex"}, installCodex, uninstallCodex},
	{"opencode", []string{".config", "opencode"}, installOpencode, uninstallOpencode},
}

// installOtherHosts registers every detected host, recording each outcome
// under its name; absent hosts are skipped so --all never creates a config dir
// for software the user lacks. Reports whether any host is now registered.
func installOtherHosts(self string, result map[string]any) (registered bool) {
	home, err := os.UserHomeDir()
	for _, h := range otherHosts {
		if err != nil {
			result[h.name] = "error: " + err.Error()
			continue
		}
		if _, statErr := os.Stat(filepath.Join(append([]string{home}, h.dir...)...)); statErr != nil {
			result[h.name] = "skipped: " + h.name + " not detected"
			continue
		}
		res, err := h.install(self)
		if err != nil {
			result[h.name] = "error: " + err.Error()
			continue
		}
		result[h.name] = res
		registered = true
	}
	return registered
}

// startGraphIndex warms the graph for the current directory in a detached
// `graph index`, so the agent's first query is not a cold build. Only a git
// root qualifies — --project installs run at the repo root.
func startGraphIndex(self string) string {
	if _, err := os.Stat(".git"); err != nil {
		return "skipped: not a git repo root"
	}
	wd, err := os.Getwd()
	if err != nil {
		return "error: " + err.Error()
	}
	// #nosec G204 -- re-exec of our own binary (os.Executable), fixed argv.
	cmd := exec.Command(self, "graph", "index", "--repo", wd)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return "error: " + err.Error()
	}
	_ = cmd.Process.Release()
	return "started"
}

// appendGraphBlock appends the code-graph guidance to path (CLAUDE.md or
// AGENTS.md), creating the file if missing. Idempotent via its own marker.
func appendGraphBlock(path string) (bool, error) {
	return appendBlock(path, graphSnippet, graphSnippetMarker)
}

// appendBlock appends block to path unless marker is already present,
// creating the file and its directory when missing.
func appendBlock(path, block, marker string) (bool, error) {
	existing, err := os.ReadFile(path) // #nosec G304 -- fixed instructions-file location, not user input
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	if strings.Contains(string(existing), marker) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return false, fmt.Errorf("create dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304
	if err != nil {
		return false, fmt.Errorf("open %s: %w", path, err)
	}
	if len(existing) > 0 {
		block = "\n" + block
	}
	if _, err := f.WriteString(block); err != nil {
		_ = f.Close()
		return false, fmt.Errorf("append %s: %w", path, err)
	}
	// Explicit Close (not defer): a write-back flush can fail and losing that
	// error silently drops appended data.
	if err := f.Close(); err != nil {
		return false, fmt.Errorf("close %s: %w", path, err)
	}
	return true, nil
}

func claudeSettingsPath(project bool) (string, error) {
	if project {
		return filepath.Join(".claude", "settings.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// buildHooksBlock builds the "hooks" object for the given command.
func buildHooksBlock(hookCmd string) map[string]any {
	hooks := map[string]any{}
	for _, e := range claudeHookEvents {
		hooks[e.name] = []any{newHookEntry(e.matcher, hookCmd)}
	}
	return map[string]any{"hooks": hooks}
}

func newHookEntry(matcher, hookCmd string) map[string]any {
	entry := map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": hookCmd}},
	}
	if matcher != "" {
		entry["matcher"] = matcher
	}
	return entry
}

// mergeHooksInto reads (or creates) settings.json, canonicalizes the session-hook
// entries, and writes it back. Returns the events touched. Idempotent: an event
// already holding exactly one current entry is left untouched. Any other
// session-hook registration — a stale binary path (e.g. a temp build that ran
// install), a drifted matcher from an older release, or a duplicate — is
// replaced, not stacked: two live registrations double-fire every hook and
// double-count the intake gate, halving the effective Stop threshold.
func mergeHooksInto(path, hookCmd string) ([]string, error) {
	settings := map[string]any{}
	if b, err := os.ReadFile(path); err == nil { // #nosec G304 -- path is the settings.json location, not user input
		if err := json.Unmarshal(b, &settings); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}

	added := []string{}
	for _, e := range claudeHookEvents {
		entries, _ := hooks[e.name].([]any)
		kept := make([]any, 0, len(entries)+1)
		canonical := false
		changed := false
		for _, entry := range entries {
			if !entryHasSessionHook(entry) {
				kept = append(kept, entry)
				continue
			}
			em, _ := entry.(map[string]any)
			matcher, _ := em["matcher"].(string)
			if !canonical && matcher == e.matcher && hookEntryExists([]any{entry}, hookCmd) {
				canonical = true
				kept = append(kept, entry)
				continue
			}
			changed = true // stale path, drifted matcher, or duplicate — dropped
		}
		if !canonical {
			kept = append(kept, newHookEntry(e.matcher, hookCmd))
			changed = true
		}
		if !changed {
			continue
		}
		hooks[e.name] = kept
		added = append(added, e.name)
	}
	settings["hooks"] = hooks

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create settings dir: %w", err)
	}
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal settings: %w", err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	return added, nil
}

// hookEntryExists reports whether any entry already registers hookCmd, so a
// re-run does not duplicate it.
func hookEntryExists(entries []any, hookCmd string) bool {
	for _, e := range entries {
		em, ok := e.(map[string]any)
		if !ok {
			continue
		}
		inner, ok := em["hooks"].([]any)
		if !ok {
			continue
		}
		for _, h := range inner {
			hm, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if cmdStr, _ := hm["command"].(string); cmdStr == hookCmd {
				return true
			}
		}
	}
	return false
}
