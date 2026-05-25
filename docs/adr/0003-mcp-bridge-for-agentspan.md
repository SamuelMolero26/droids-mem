# 0003 — MCP bridge for agentspan integration

**Status**: Amended
**Date**: 2026-05-17
**Amended**: 2026-05-24

## Context

`droids-mem` ships as a Go CLI binary. Agents with a shell tool (Claude Code, Codex CLI, Cursor, Aider) can already invoke it via subprocess and parse JSON. That covers local-dev and CLI-native agents but not the target deployment platform: **agentspan**, a distributed, durable agent runtime where workers may pause for human approval for days and resume on different machines.

Agentspan exposes four tool types: in-worker `@tool` (Python), and server-side `http_tool`, `api_tool` (OpenAPI), and `mcp_tool` (MCP server URL). The user explicitly wants to keep the Go stack — no Python rewrite, no HTTP/OpenAPI surface for V1 — and just needs a bridge so agentspan-deployed agents can read and write memories.

This requires deciding: how does droids-mem present itself to a remote agent runtime without losing the local-first, single-binary character of V1?

## Decision

Add a second Go binary, `cmd/droids-mem-mcp/`, that exposes the existing `internal/store` package as an MCP server. The CLI binary is untouched. Both binaries share the same `internal/store` and `internal/db` packages — no duplicated business logic.

**Stack additions:**
- `github.com/mark3labs/mcp-go` — Go MCP SDK supporting stdio + HTTP transports
- No CGO, no Python, no new persistence layer

**Transport:** HTTP (long-running server) is the agentspan target. Stdio remains available for local development and ad-hoc inspection.

**Auth:** Bearer token, validated per HTTP request against `DROIDS_MEM_MCP_TOKEN` env var, constant-time compare. Missing or mismatched token returns 401. No multi-tenant identity in V1 — the token gates access to the entire local DB.

**Tools exposed (4, not all 7 subcommands):**
- `mem_save`
- `mem_search`
- `mem_context`
- `mem_get`

`list`, `schema`, and `doctor` remain CLI-only — they are operator/introspection commands, not agent-facing.

**Session ownership — hybrid agent-owned:**

The MCP server is stateless. No per-connection session map.

- Agent calls `mem_context` at Run start → server mints a new ULID `session_id` and returns it in the response payload alongside the always- and browse-tier memories.
- Agent stashes `session_id` in its own durable state (agentspan persists this automatically across pauses and worker swaps).
- Agent passes `session_id` explicitly on every subsequent `mem_save` call in the Run.
- Final `mem_save` of kind `session_summary` uses the same `session_id`; retention prunes oldest if count exceeds 5 for that `task_type`.

This means the `mem_context` response shape gains a `session_id` field — minor additive change to the bundle defined in ADR 0002.

**DB:** SQLite local to the MCP host. V1 assumes a single MCP server pinned to one machine. Multi-host distribution is deferred (see Future.md).

## Consequences

**Accepted**

- Go-only stack preserved. No language rewrite, no FFI, no new persistence layer.
- Existing CLI contract is untouched — current CLI agents and tests keep working unchanged.
- `internal/store` is the single source of truth for both transports; bugs fixed once apply everywhere.
- Stateless server survives durable-agent reconnects, worker swaps, and human-approval pauses — agentspan's main value prop.
- Bearer auth is trivially deployable behind any private-network or reverse-proxy setup.
- Adding new MCP tools later (e.g., `mem_list` for ops dashboards) is additive.

**Tradeoffs**

- A second binary means two build targets, two release artifacts, and two deployment surfaces.
- SQLite local + single MCP host means horizontal scale of the server is blocked until the DB layer changes. Acceptable for V1; revisit when load justifies.
- Single-token auth has no per-agent identity, audit trail, or scope control. Acceptable for trusted-network deployments only.
- `mem_context` response gains a `session_id` field — additive but a wire-shape change for any caller already integrated.
- Per-connection session tracking is rejected, so an agent that "forgets" to thread `session_id` through its calls will scatter memories across orphan sessions. Mitigated by making `mem_context` the canonical Run-start call that returns the id.

## Alternatives considered

- **Python `@tool` wrapper subprocessing the binary** — fastest to ship but forces a Python worker on every agentspan deployment, couples deployment topology to Python, and adds a process-spawn per call. Rejected as a long-term answer; acceptable as a temporary local-dev hack.
- **HTTP/REST API in Go (no MCP)** — needs OpenAPI authoring + client codegen on the agentspan side. More boilerplate than MCP for the same outcome.
- **Per-connection session state on the server** — simpler for ephemeral agents but breaks the moment an agentspan worker pauses for approval and resumes elsewhere. The durability property of agentspan makes this a non-starter.
- **Expose every CLI subcommand as an MCP tool (`mem_list`, `mem_schema`, `mem_doctor`)** — pollutes the agent's tool budget with operator commands the agent never needs. Hidden by default; can be added behind a flag later.
- **Embed MCP transport into the existing `droids-mem` binary as a subcommand (`droids-mem serve --mcp`)** — viable; rejected to keep the CLI binary single-purpose and to allow the MCP server to be deployed independently (e.g., as a systemd unit or container) without dragging in CLI flag-parsing surface. **This alternative was later adopted — see Amendment below.**

## Amendment — 2026-05-24: collapse to single binary

`cmd/droids-mem-mcp/` has been deleted. `droids-mem serve` is now the canonical way to start the MCP bridge.

**What changed:** The two-binary decision was reversed for two reasons:

1. **`ensure-server` requires a single binary.** `ensure-server` achieves zero-config startup by re-executing its own path (`os.Executable()`) as `droids-mem serve`. With a separate `droids-mem-mcp` binary, `ensure-server` would need to know the path to a different artifact — breaking zero-config and requiring manual PATH or env-var setup. Single binary is the only layout where `ensure-server` works without configuration.

2. **Two build artifacts were operationally painful.** Two binaries meant two install steps, two version-sync requirements, and two deployment surfaces. In V1 (single-machine, local-first), the independence this bought was never exercised.

**Token contract — Agent client side:** `DROIDS_MEM_MCP_TOKEN` is no longer required as an env var for Agent clients. The fallback path reads `~/.droids-mem/token` (written by the bridge on first start); zero-config deployments need no env var at all. The server-side auth decision is unchanged — the bridge still validates a bearer token on every request.

**What did not change:** Transport, tools, session ownership, and DB assumptions are unchanged. The `internal/mcpserver` package still isolates all bridge logic — the merge was a binary boundary change, not a package architecture change.
