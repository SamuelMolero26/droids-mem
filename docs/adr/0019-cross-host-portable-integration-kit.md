# 0019 — Cross-host portable integration kit (any MCP agent)

**Status**: Proposed
**Date**: 2026-06-24
**Extends**: ADR-0016 (native Claude Code session auto-summary)

## Context

ADR-0016 made droids-mem *self-invoking* for one consumer: Claude Code. It does
so through **host hooks** (`PostToolUse`, `Stop`, `SessionEnd`, `SessionStart`,
`UserPromptSubmit`) that drive the **CLI subprocess** (`droids-mem session
hook`/`stage`). That stack is enforced and guaranteed — and entirely
Claude-Code-specific: the hooks live in `settings.json`, not the binary, and no
other host has them.

The next ask is broader: let an agent on *any* MCP host call droids-mem without
the user prompting it — and do it through the **MCP tools** (`mem_*`), not the
CLI hooks. This runs into a hard limit:

- **MCP has no auto-call primitive.** The host's model decides when to call a
  tool. A server cannot make a client invoke it.
- The only thing a server can do that **every** compliant host surfaces to its
  model is the `initialize`-response **`instructions` field** plus per-tool
  **descriptions**. droids-mem sets neither at the server level today
  (`internal/mcpserver/server.go:76` calls `server.NewMCPServer` with no
  `WithInstructions`).
- Hard enforcement (guaranteed save-at-exit, guaranteed recall-at-prompt,
  mechanical intake gate) requires host hooks — which are inherently per-host and
  do not port. Hosts without a hook system *cannot* be given enforcement at all.

So "native, no user prompting, any agent" cannot mean one mechanism. It forces a
**layered** model, and the load-bearing decision is how high the *portable* layer
can honestly reach.

## Decision

**1. Two layers. Portable best-effort is the product; per-host enforcement is an
opt-in adapter pattern.**

- **Layer 1 — portable, every MCP host.** A server `instructions` block
  (`server.WithInstructions`) + sharpened tool descriptions. The model is *told*
  the protocol and good models comply, exactly as `context7`-style
  servers nudge a model today. This is **best-effort**: nothing is guaranteed,
  and the floor is **model judgment**, backstopped by the store's existing
  dedup. Zero host-specific code.
- **Layer 2 — per-host enforcement adapter, opt-in.** Real guarantees need host
  hooks. **ADR-0016 is the reference Layer-2 adapter** (Claude Code). Every other
  host needs its own; hosts with no hook system get Layer 1 only.

The promise "any agent" therefore resolves to **Layer-1 best-effort everywhere,
hard guarantees only where a host exposes hooks.** We do not fake enforcement on
hosts that lack the signal for it.

**2. Recall is relevance-first, not `task_type`-gated.** The portable protocol
leads with `mem_search(query)` over the task description (no `task_type`), which
ranks by BM25 across the whole corpus. `mem_context(task_type)` is the *curated
continuity* layer (always-tier user_rules + last_session), used only when a
stable workflow slug exists. A slug miss **degrades gracefully** — it skips
continuity; the search path still surfaces prior lessons. This avoids the
cold-start trap where a new project (or a drifting per-run slug) makes
`mem_context` permanently empty even though memories exist. Consistent with
ADR-0016 decision points 7–8 (agent gets memories by relevance, not a
task_type/recency push). No Layer-1 relevance floor is needed: model judgment is
the floor; the mechanical floor stays a Layer-2/hook concern.

**3. Proactive save floor = model judgment + existing dedup.** ADR-0016's
mechanical intake gate (change-count ≥ N) is hook-derived = Layer 2 only. Layer 1
has no portable change signal to count, so it does **not** try to fake one. The
portable save protocol is "save only a reusable lesson — a fix, a decision, a
convention," and repeat spam is killed by the pre-existing dedup (fingerprint
exact + Jaccard ≥ 0.85 near-duplicate). On Claude, `session_summary` stays
hook-handled (ADR-0016) to avoid double-saves; the Layer-1 `instructions` focus
the model on the other three kinds in-run.

**4. A Session is a grouping key, never a stored row — so "save on session open"
is rejected.** `mem_context` mints `session_id` read-only (`tools.go:172-176`,
`BEGIN DEFERRED`); opening a session writes nothing. Rows appear only when a
`mem_save` uses the id. Therefore:

- **"Linked to the session"** is already native (ADR-0003): mint on
  `mem_context`, thread through every `mem_save`. No new machinery.
- **The save trigger is at close/checkpoint, never at open.** At open there is
  nothing to save; a save-on-open trigger is precisely how empty rows get
  manufactured. Open-time work is *recall* + recovery-flush, not save.
- **"Avoid empty sessions" is free by construction**: no `mem_save` → no rows.
  An empty session is physically incapable of persisting anything; there is no
  empty row to suppress. The ADR-0016 intake gate is a second filter on the
  hook flush path, not the primary defense.

**5. stdio transport is the portability key, deferred behind Claude.** Layer 1
only reaches hosts that can *speak* to the bridge. `serve` is streamable-HTTP +
bearer only (`NewStreamableHTTPServer`), which fits the persistent shared-daemon
model but forces every host to configure a port, token, and `ensure-server`.
Most MCP hosts mount **stdio** servers (host spawns the binary, talks over pipes,
manages lifecycle — no port/token/daemon). A `serve --stdio` entrypoint
(`server.ServeStdio`, same tool registration) is the single highest-leverage code
change for true "any agent" reach; concurrency across stdio instances on one
`mem.db` is ordinary SQLite multi-process locking + `busy_timeout`. **Scoped
out for now** — Claude Code already speaks HTTP, so the Layer-1 `instructions`
field ships first against Claude; stdio + opencode/codex adapters are tracked in
`future.todo`.

## Consequences

**Accepted**

- The MCP surface stays at 4 tools (ADR-0016 decision 7 holds); the only Layer-1
  code is `WithInstructions` + tool-description edits — additive, reversible.
- The data model is untouched: no session row, no new column, no new kind.
- Recall works on brand-new projects and topic pivots because it does not depend
  on a `task_type` slug.

**Tradeoffs**

- "Any agent" honestly means best-effort everywhere, guaranteed only on hosts
  with a Layer-2 adapter. Documented, not papered over.
- Two proactive surfaces coexist on Claude (CLI hooks + MCP instructions). Kept
  non-redundant by division: hooks own `session_summary`; MCP instructions own
  the other three kinds + recall.
- Portable enforcement is impossible on hook-less hosts; we accept the model as
  the only floor there.

## Alternatives considered

- **Hard guarantees on every host** — rejected. Requires a hook system the host
  may not have; not portable by construction.
- **Fake a change-count intake gate at Layer 1** — rejected. No portable signal
  to count; dedup + model judgment already cover repeat/low-value spam.
- **Gate portable recall on `task_type`** — rejected. Brittle on new projects and
  slug drift; relevance search needs no slug and degrades gracefully.
- **Save on SessionStart** — rejected. Nothing exists to save at open; it
  manufactures the empty rows the requirement exists to prevent.
- **stdio-first / HTTP-only forever** — deferred, not rejected: HTTP ships first
  (Claude already speaks it); stdio is the documented next step for opencode and
  codex.

## Open items

- The exact `instructions` wording — tune against real host compliance once the
  Layer-1 build lands.
- stdio transport (`serve --stdio`) and the opencode / codex adapters —
  tracked in `future.todo` (Cross-host MCP integration kit).
- Whether per-tool description sharpening alone moves compliance enough to make
  the server `instructions` block optional — measure, do not assume.

## Addendum — agent-first friction fixes (2026-07-02)

An agent-first usability review found gaps this ADR's original scope left open.
Resolved as follows:

1. **Registration bootstrap.** This ADR started from "tools already exist" and
   never covered how they come to exist on a fresh host. `droids-mem install
   --all` now performs the full Claude bootstrap in one idempotent command:
   hooks → `ensure-server` → `claude mcp add --scope user` → append the
   compose-guidance block (embedded `cmd/droids-mem/claude_snippet.md`, the
   canonical copy) to `~/.claude/CLAUDE.md`. MCP itself has no
   self-registration primitive, so driving the client's own CLI is the ceiling;
   non-Claude hosts remain manual until their adapters land.
2. **Daemon liveness for Layer 1.** Hooks talk to the store directly, but the
   model's own `mem_*` calls need `serve` up — after a reboot nothing restarted
   it. The `SessionStart` hook now runs `ensure-server` (best-effort, fails
   open) before orphan recovery.
3. **Recall floor made real — and re-based.** The shipped
   `DefaultRelevanceFloor = 0.0` was a no-op gate, violating ADR-0016 pt 8
   ("no match above floor → inject nothing"): search terms are OR-joined, so
   any memory sharing one common word matched. An absolute BM25 floor cannot
   fix this — rank magnitudes scale with corpus size (FTS5 IDF ≈ 0 on tiny
   DBs). The gate is now prompt-token overlap (`store.TokenOverlap`,
   corpus-size-invariant), default 0.3; still provisional until the T1.2
   recall eval.
4. **session_id re-threading.** "Call recall again on topic pivot" plus
   `mode=refresh` combined into a silent fragmentation hole: a repeat
   `mem_context` without the prior session_id mints a new one. The server
   `instructions` and the `session_id` parameter description now say
   explicitly: pass the existing id on any repeat call in the same Run.
5. **Imperative tool descriptions.** Hosts that drop `initialize.instructions`
   previously fell back to passive capability blurbs. Each of the 4 tool
   descriptions now carries its own "call this on your own" clause, and the
   `task_type` hint is mechanical (repo/directory name, reuse the exact
   string) to curb write-side slug drift.
