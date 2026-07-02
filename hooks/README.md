# droids-mem ↔ Claude Code session memory (ADR-0016)

Makes session memory **guaranteed**, not best-effort — driven by Claude Code
hooks, all handled natively by the `droids-mem` binary. **No shell scripts, no
`jq`.**

## Install

```
droids-mem install                       # merge hooks into ~/.claude/settings.json
cat hooks/session-memory.md >> ~/.claude/CLAUDE.md   # compose guidance for the model
```

`install` is idempotent and preserves your existing settings. Use `--project`
for `./.claude/settings.json`, or `--print` to preview the block. (To also give
the agent the `mem_*` tools, register the MCP bridge — see the main
[README](../README.md#use-with-claude-code).)

## How it works

One binary command, `droids-mem session hook`, reads the hook JSON on stdin and
dispatches on `hook_event_name`:

| Event | Action |
|-------|--------|
| `PostToolUse` (Edit/Write/Bash/…) | bump the meaningful-change counter (intake gate, DB-free) |
| `Stop` | block once + ask the model to stage when work is unstaged (checkpoint) |
| `SessionEnd` | flush the staged summary if the gate passes; clear sentinels |
| `SessionStart` | flush orphaned summaries from crashed runs; sweep stale |
| `UserPromptSubmit` | inject relevant prior memories above a floor, deduped |

Composition (the model-judgment half of the intake gate) lives in
[`session-memory.md`](./session-memory.md) — added to your `CLAUDE.md` by the
install step above.

## Verify (manual — hooks run only in the Claude Code runtime)
1. Start a session; make ≥3 edits; end it.
2. `droids-mem recent-sessions` → your auto-summary appears (`origin=auto`,
   `task_type=claude_session`).
3. Start a new session and ask about prior work → relevant memories are injected.

## Notes
- The hook JSON field names (`session_id`, `prompt`, `tool_name`) follow the
  Claude Code hooks spec; verify against your version.
- Every hook path **fails open** — a memory hiccup never breaks your session.
- The relevance **floor** (default `0.3` — at least ~a third of the prompt's
  meaningful tokens must appear in the memory) is tunable via
  `session pull --floor`; provisional until the T1.2 recall eval lands
  (ADR-0016 open item). Search terms are OR-joined, so without the floor a
  memory sharing one common word with the prompt would be injected.
