package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// hookEvent maps a Claude Code hook event to an optional tool matcher. A matcher
// limits how often the hook fires (fewer binary spawns); event-less hooks fire
// every time.
type hookEvent struct {
	name    string
	matcher string
}

var claudeHookEvents = []hookEvent{
	{"PostToolUse", "Edit|Write|MultiEdit|NotebookEdit|Bash"},
	{"Stop", ""},
	{"SessionEnd", ""},
	{"SessionStart", ""},
	{"UserPromptSubmit", ""},
}

// newInstallCmd wires droids-mem session memory into Claude Code in one shot:
// it merges the hook entries into settings.json, idempotently, pointing every
// event at `<this binary> session hook`. No shell scripts, no jq.
func newInstallCmd() *cobra.Command {
	var project, printOnly bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Wire droids-mem session memory into Claude Code (settings.json hooks)",
		Long: "Merge the session-memory hooks into Claude Code's settings.json.\n" +
			"Default target is the user settings (~/.claude/settings.json); use\n" +
			"--project to target ./.claude/settings.json instead. Idempotent and\n" +
			"non-destructive — existing settings and hooks are preserved.",
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
			writeJSON(map[string]any{
				"status":       "installed",
				"settings":     path,
				"events_added": added,
				"command":      hookCmd,
				"next_step":    "append hooks/session-memory.md to your CLAUDE.md so the model knows when to stage summaries",
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&project, "project", false, "Install into ./.claude/settings.json instead of the user settings")
	cmd.Flags().BoolVar(&printOnly, "print", false, "Print the hooks block instead of writing settings.json")
	return cmd
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
