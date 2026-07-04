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

	"github.com/spf13/cobra"

	"github.com/samuelmolero26/droids-mem/internal/mcpserver"
	"github.com/samuelmolero26/droids-mem/internal/state"
)

// claudeSnippet is the CLAUDE.md compose-guidance block (the model-judgment
// half of the intake gate, ADR-0016). Embedded so `install --all` can append
// it without needing the repo checkout. This file is the single source of the
// block; hooks/session-memory.md points here rather than duplicating it.
//
//go:embed claude_snippet.md
var claudeSnippet string

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
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Wire droids-mem session memory into Claude Code (settings.json hooks)",
		Long: "Merge the session-memory hooks into Claude Code's settings.json.\n" +
			"Default target is the user settings (~/.claude/settings.json); use\n" +
			"--project to target ./.claude/settings.json instead. Idempotent and\n" +
			"non-destructive — existing settings and hooks are preserved.\n\n" +
			"--all performs the full bootstrap in one shot: hooks + start the MCP\n" +
			"bridge + register it with the Claude Code CLI (user scope) + append\n" +
			"the compose-guidance block to CLAUDE.md. Each step is idempotent.",
		RunE: func(cmd *cobra.Command, args []string) error {
			self, err := os.Executable()
			if err != nil {
				writeError("install_failed", "cannot resolve binary path: "+err.Error(), false)
				exitWith(ExitError)
			}
			hookCmd := self + " session hook"

			if printOnly {
				writeJSON(buildHooksBlock(hookCmd))
				return nil
			}

			path, err := claudeSettingsPath(project)
			if err != nil {
				writeError("install_failed", err.Error(), false)
				exitWith(ExitError)
			}
			added, err := mergeHooksInto(path, hookCmd)
			if err != nil {
				writeError("install_failed", err.Error(), true)
				exitWith(ExitError)
			}
			result := map[string]any{
				"status":       "installed",
				"settings":     path,
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
			result["server"] = stepStatus(runEnsureServer(self))
			result["mcp_registration"] = stepStatus(registerClaudeMCP())
			mdPath, appended, err := appendClaudeSnippet(project)
			switch {
			case err != nil:
				result["claude_md"] = "error: " + err.Error()
			case appended:
				result["claude_md"] = "appended: " + mdPath
			default:
				result["claude_md"] = "already_present: " + mdPath
			}
			writeJSON(result)
			return nil
		},
	}
	cmd.Flags().BoolVar(&project, "project", false, "Install into ./.claude/settings.json instead of the user settings")
	cmd.Flags().BoolVar(&printOnly, "print", false, "Print the hooks block instead of writing settings.json")
	cmd.Flags().BoolVar(&all, "all", false, "Full bootstrap: hooks + ensure-server + claude mcp add + CLAUDE.md snippet")
	return cmd
}

// stepStatus renders a bootstrap step outcome for the result JSON.
func stepStatus(err error) string {
	if err != nil {
		return "error: " + err.Error()
	}
	return "ok"
}

// runEnsureServer starts (or confirms) the MCP bridge via `<self> ensure-server`.
func runEnsureServer(self string) error {
	// #nosec G204 -- re-exec of our own binary (os.Executable), fixed argv.
	if out, err := exec.Command(self, "ensure-server").CombinedOutput(); err != nil {
		return fmt.Errorf("ensure-server: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// registerClaudeMCP registers the bridge with the Claude Code CLI at user scope
// (available in every project) unless already registered. Registration is the
// one bootstrap step MCP itself cannot do — a server cannot add itself to a
// client's config — so we drive the client's own CLI.
func registerClaudeMCP() error {
	claude, err := exec.LookPath("claude")
	if err != nil {
		return errors.New("claude CLI not found in PATH — register manually: claude mcp add --scope user --transport http droids-mem <url> --header 'Authorization: Bearer <token>'")
	}
	// #nosec G204 -- claude path from exec.LookPath, fixed argv.
	if exec.Command(claude, "mcp", "get", "droids-mem").Run() == nil {
		return nil // already registered
	}
	tok, err := state.LoadOrCreateToken()
	if err != nil {
		return fmt.Errorf("load token: %w", err)
	}
	url := baseURL(envOr("DROIDS_MEM_MCP_ADDR", mcpserver.DefaultAddr)) +
		envOr("DROIDS_MEM_MCP_ENDPOINT", mcpserver.DefaultEndpoint)
	// #nosec G204 -- claude path from exec.LookPath, token is our own bearer.
	out, err := exec.Command(claude, "mcp", "add",
		"--scope", "user",
		"--transport", "http",
		"droids-mem", url,
		"--header", "Authorization: Bearer "+tok,
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
	if project {
		path = "CLAUDE.md"
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false, fmt.Errorf("resolve home dir: %w", err)
		}
		path = filepath.Join(home, ".claude", "CLAUDE.md")
	}
	existing, err := os.ReadFile(path) // #nosec G304 -- fixed CLAUDE.md location, not user input
	if err != nil && !os.IsNotExist(err) {
		return path, false, fmt.Errorf("read %s: %w", path, err)
	}
	if strings.Contains(string(existing), claudeSnippetMarker) {
		return path, false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return path, false, fmt.Errorf("create dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304
	if err != nil {
		return path, false, fmt.Errorf("open %s: %w", path, err)
	}
	block := claudeSnippet
	if len(existing) > 0 {
		block = "\n" + block
	}
	if _, err := f.WriteString(block); err != nil {
		_ = f.Close()
		return path, false, fmt.Errorf("append %s: %w", path, err)
	}
	// Explicit Close (not defer): a write-back flush can fail and losing that
	// error silently drops appended data.
	if err := f.Close(); err != nil {
		return path, false, fmt.Errorf("close %s: %w", path, err)
	}
	return path, true, nil
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

// mergeHooksInto reads (or creates) settings.json, adds any missing session-hook
// entries, and writes it back. Returns the events newly added. Idempotent: an
// event already pointing at hookCmd is left untouched.
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
		if hookEntryExists(entries, hookCmd) {
			continue
		}
		hooks[e.name] = append(entries, newHookEntry(e.matcher, hookCmd))
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
