package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/samuelmolero26/droids-mem/internal/state"
	"github.com/spf13/cobra"
)

// hookInput is the subset of the Claude Code hook stdin JSON this adapter reads.
// Field names follow the Claude Code hooks spec (verify against your version).
type hookInput struct {
	HookEventName string `json:"hook_event_name"`
	SessionID     string `json:"session_id"`
	Prompt        string `json:"prompt"`
	ToolName      string `json:"tool_name"`
	// ToolInput is the tool's argument object; only the file-path fields matter
	// here (file-provenance capture, ADR-0021 Phase 2).
	ToolInput struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
	} `json:"tool_input"`
	// StopHookActive is true when the turn is already continuing because a Stop
	// hook blocked it. Blocking again while it's set loops until the host's cap
	// force-ends the turn, so the Stop case must stand down.
	StopHookActive bool `json:"stop_hook_active"`
}

// meaningfulTools are the PostToolUse tools that count toward the intake gate
// (ADR-0016 pt 5) — file edits only. Bash is deliberately excluded: trivial
// shell commands (ls, git status, go test) tripped the Stop gate on read-only
// sessions, forcing a summary turn where nothing changed.
var meaningfulTools = map[string]bool{
	"Edit": true, "Write": true, "MultiEdit": true, "NotebookEdit": true,
}

// fileTools are the PostToolUse tools whose tool_input names a file the session
// read or changed (ADR-0021 Phase 2 provenance). Includes Read — a file the
// session read is provenance too — but not Bash (its arg is a command, not a
// path).
var fileTools = map[string]bool{
	"Read": true, "Edit": true, "Write": true, "MultiEdit": true, "NotebookEdit": true,
}

// hookFilePath extracts the touched file path from a hook's tool_input, or ""
// when the tool carries none.
func hookFilePath(in hookInput) string {
	if !fileTools[in.ToolName] {
		return ""
	}
	if in.ToolInput.FilePath != "" {
		return in.ToolInput.FilePath
	}
	return in.ToolInput.NotebookPath
}

// newSessionHookCmd is the native Claude Code hook entry point: one command,
// driven by the hook JSON on stdin, dispatched on hook_event_name. Replaces the
// jq shell glue — no external dependency, fully testable. `settings.json` points
// every hook event at `droids-mem session hook`.
//
// Every path fails open (exit 0, best-effort): a memory hiccup must never break
// the user's Claude Code session.
func newSessionHookCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "hook [event]",
		Short: "Claude Code hook entry point (reads hook JSON on stdin; dispatches by event)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, _ := io.ReadAll(cmd.InOrStdin())
			var in hookInput
			_ = json.Unmarshal(raw, &in) // tolerate malformed input — fail open

			event := in.HookEventName
			if len(args) == 1 {
				event = args[0] // explicit override (testing / when stdin lacks it)
			}

			switch normalizeEvent(event) {
			case "posttooluse":
				if in.SessionID != "" && meaningfulTools[in.ToolName] {
					_, _ = state.IncrementChange(in.SessionID)
				}
				if in.SessionID != "" {
					if fp := hookFilePath(in); fp != "" {
						_ = state.AppendFiles(in.SessionID, []string{fp})
					}
				}
			case "stop":
				if in.SessionID != "" && !in.StopHookActive && sessionNeedsStage(in.SessionID) {
					emitStopBlock(in.SessionID)
				}
			case "sessionend":
				if in.SessionID != "" {
					if s, err := a.store(); err == nil {
						_ = flushSession(cmd.Context(), s, in.SessionID)
						_ = state.ClearSession(in.SessionID)
					}
				}
			case "sessionstart":
				// No server to start: the host spawns `serve --stdio` itself and
				// owns its lifecycle. This used to re-exec ensure-server, which
				// meant every `claude` CLI invocation resurrected a listener
				// nobody connects to.
				if s, err := a.store(); err == nil {
					recoverOrphans(cmd.Context(), s)
				}
			case "userpromptsubmit":
				if in.SessionID != "" && strings.TrimSpace(in.Prompt) != "" {
					if s, err := a.store(); err == nil {
						writeRelevanceText(relevancePull(cmd.Context(), s, in.SessionID, in.Prompt, DefaultRelevanceFloor))
					}
				}
			}
			return nil
		},
	}
}

// normalizeEvent canonicalizes a hook event name to a lowercase, separator-free
// token so "PostToolUse", "post-tool-use", and "post_tool_use" all match.
func normalizeEvent(e string) string {
	e = strings.ToLower(e)
	e = strings.ReplaceAll(e, "-", "")
	e = strings.ReplaceAll(e, "_", "")
	return e
}

// emitStopBlock prints the Claude Code Stop-hook block decision, re-prompting the
// model to compose + stage its progress (ADR-0016 pt 2 checkpoint enforcement).
func emitStopBlock(ccID string) {
	reason := fmt.Sprintf("Before stopping, record session progress: compose a concise summary "+
		"(title, what happened, what to remember next time) and stage it with "+
		"`droids-mem session stage --session %s --title ... --what ... --learned ...`. "+
		"If nothing this session is worth recalling, run "+
		"`droids-mem session decline --session %s` instead.", ccID, ccID)
	b, err := json.Marshal(struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}{Decision: "block", Reason: reason})
	if err != nil {
		return // fail open: a hook must never break the user's session
	}
	fmt.Println(string(b))
}
