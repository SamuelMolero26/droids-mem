# 0005 — Three-layer workspace model

**Status**: Deferred to v1.1
**Date**: 2026-06-08
**Last Updated**: 2026-06-08

> **v1.0 amendment (2026-06-08):** Implementation deferred to v1.1 per [v1.0 implementation plan](../v1.0-implementation-plan.md). When this ADR ships, the **user workspace skips the `local/` subdir** — user DB stays at `~/.droids-mem/mem.db` (historical path) instead of `~/.droids-mem/local/mem.db`. Workflow workspaces still under `~/.droids-mem/workspaces/<name>/`. Trade: asymmetric layout vs zero migration of existing installs. §4.2 + §6.6 storage tables read with that override.

## Context

V1 of `droids-mem` ships a single global memory store at `~/.droids-mem/mem.db`, resolved via `DROIDS_MEM_DB` / `DROIDS_MEM_HOME` env vars. One database, one MCP server, one bearer token. This matches the "engram-style" pattern: a per-user, always-on memory layer attached to whichever agent the user is currently driving.

Two real-world workloads are not served by that single-store model:

1. **Shared repository memory.** A developer working with an agent on a repo learns project-specific patterns (conventions, error fixes, design decisions). Today those memories live in `~/.droids-mem/` on one machine. A coworker cloning the repo gets none of that context — their agent re-learns from zero. Engram, mem0, and other incumbents do not address this: memory is per-user, not per-project.

2. **Continuous workflow agents.** Long-running automation agents (email triage bot, scheduled PR reviewer, cron-driven monitor) are not tied to a repository or a developer's interactive session. They need their own memory pool — isolated from the developer's interactive memory, isolated from other bots, surviving process restarts, runnable on machines without any repo checked out.

Bolting these onto the single-store model breaks down quickly:

- Cramming repo memory into `~/.droids-mem/mem.db` and exporting via ad-hoc scripts loses scope semantics (which rows belong to which project? which are personal?) and pollutes FTS5 ranking across unrelated codebases.
- Spawning a second `droids-mem serve` instance per bot with `DROIDS_MEM_DB=/path/to/bot.db` works mechanically but has no first-class config, no discovery, no lifecycle story, no token isolation, and no shared CLI/TUI affordances.
- Letting agents pick the DB path per-call leaks workspace concerns into the MCP tool surface and forces every consumer to re-implement discovery.

The user explicitly wants all three workloads supported on a local-first substrate (no SaaS, no required network) with the engram-style interactive UX preserved as the default.

## Decision

Introduce **workspace** as a first-class concept. A workspace is a named, configured memory boundary backed by exactly one SQLite database. Three workspace **types** are defined, all sharing the same schema, store layer, and MCP tool surface:

| Type | Purpose | Storage | MCP lifecycle | Sync |
|------|---------|---------|---------------|------|
| `user` | Engram-style per-user interactive memory. Always-on, attached to the dev agent. Default for `mem_*` calls when no other workspace resolves. | `~/.droids-mem/local/mem.db` | One long-lived server, default `:7777`. | None. |
| `project` | Repo-scoped memory. Initialized in a repo, committed via git, picked up by coworkers on clone. Auto-discovered via cwd walk-up. | `<repo>/.droids-mem/mem.db` (gitignored) + `<repo>/.droids-mem/memories.jsonl` (committed). | Discovered and merged into the `user` workspace's MCP at query time. No separate server. | `git-jsonl` (default). Exports `scope=shared` memories to `memories.jsonl`; rebuilds DB from JSONL on `sync`. |
| `workflow` | Continuous-agent memory. Named, isolated, no repo binding. One workspace per bot. | `~/.droids-mem/workspaces/<name>/mem.db` | Dedicated server per workspace (`droids-mem serve --workspace <name>`), separate port and bearer token. | None by default; configurable. |

**Each workspace has a `workspace.yml`** that declares its name, type, MCP settings, sync mode, retention, and merge rules. The yml is the source of truth for workspace configuration; CLI flags and env vars override per-invocation.

**Resolution order** for any `mem_*` call:

1. Explicit `--workspace <name>` flag or `DROIDS_MEM_WORKSPACE` env var.
2. `cwd` walk-up: nearest `.droids-mem/workspace.yml` ⇒ `project` workspace.
3. Fallback: `user` workspace.

**Merge semantics** for `user` + discovered `project`:

- The `user` workspace MCP, when `auto_discover_repos: true`, detects a `project` workspace via cwd walk-up at request time and opens its DB read-write.
- `mem_context` / `mem_search` queries return the **union** of `user` + `project` rows, each tagged with a `workspace_source` field so the TUI and consumers can distinguish origin.
- On collision (same fingerprint exists in both), `project` wins — more specific to current work — but the merged result includes both IDs so the TUI can surface the duplication.
- `mem_save`:
  - `scope=personal` ⇒ always written to `user` workspace only.
  - `scope=shared` ⇒ written to the resolved workspace (typically `project` when one is in scope; otherwise `user`).
  - On `scope=shared` save to a `project` workspace with `sync.mode: git-jsonl`, the JSONL export is queued and flushed before the call returns.

**Schema addition.** A `scope` column is added to `memories`:

```
scope TEXT NOT NULL CHECK (scope IN ('personal','shared')) DEFAULT 'shared'
```

`scope=shared` is the default because the majority of distilled memories (error resolutions, task patterns, session summaries) apply across collaborators. `scope=personal` is opt-in for user-style preferences ("I prefer tabs", "always show diffs in unified format").

**Workspace bootstrap commands:**

- `droids-mem init` — run inside a repo. Creates `.droids-mem/workspace.yml` (`type: project`), seeds `.gitignore` (db, token, pid, log) and `memories.jsonl` placeholder, registers workspace.
- `droids-mem workspace create <name> --type workflow` — provisions a workflow workspace under `~/.droids-mem/workspaces/<name>/`.
- `droids-mem workspace list` / `droids-mem workspace remove <name>` — registry operations.

**Token isolation.** Each workspace owns its own bearer token, written to its workspace dir at `0600`. A workflow bot's token cannot read the `user` workspace, and vice versa. Project workspaces sit inside the `user` MCP's trust boundary (same process, same token), since the user is the one driving the agent.

**TUI scope.** `droids-mem tui` opens the `user` workspace by default. `--workspace <name>` switches; `--merged` shows the union view with workspace badges per row. Edits respect the row's `workspace_source`.

## Consequences

**Accepted**

- The single-store V1 deployment continues to work: it is simply the `user` workspace with no project workspaces discovered and no workflow workspaces registered. Existing installs migrate by moving `~/.droids-mem/mem.db` to `~/.droids-mem/local/mem.db` and writing a default `workspace.yml`. A one-shot `droids-mem migrate` covers this.
- Coworker onboarding becomes trivial: `git clone && droids-mem sync && agent runs`. The repo is the distribution channel for project memory.
- Continuous-workflow agents get a first-class story: named workspace, dedicated MCP, isolated token, no repo dependency. Bots can run on machines without any source checked out.
- All four ADR-0004 contracts (4-kind enum, Root-only writer, parent-as-broker, no `observation` kind) are unchanged. Workspaces partition *where* writes land, not *what* is written.
- The MCP tool surface (`mem_save`, `mem_search`, `mem_context`, `mem_get`) is unchanged. Workspace resolution happens server-side before any tool dispatch.

**Tradeoffs**

- `scope` adds a column and a code path to every save. Workflow workspaces never use `scope=personal` meaningfully; the column is dead weight for them. Acceptable: one nullable enum column is cheap, and uniformity simplifies the store layer.
- Merge-at-query-time (`user` + `project`) means each `mem_context` call opens two DB handles. Mitigated by SQLite's lightweight connection cost and SQLite WAL mode; benchmark before V2.
- Project workspaces committed to git expose `memories.jsonl` to PR review. This is a feature (reviewable agent learnings) but adds friction: bad agent-saved memories now require code review to remove, not just `mem delete`. Mitigated by `scope=personal` default for any rule whose blast radius is unclear, and by an explicit `droids-mem sync --no-export` escape hatch.
- Single-writer ADR-0004 holds per-workspace, not globally. Two agents writing to two different workspaces concurrently is fine. Two agents writing to the same `project` workspace concurrently (e.g., dev machine + CI) is still racy; documented as out-of-scope for V1 of the workspace model.
- Workflow MCP lifecycle (per-workspace ensure-server, pid file, log file) multiplies the operational surface. `droids-mem workspace status` and per-workspace `doctor` are required to keep this debuggable.

## Alternatives considered

- **Single-store with a `project` / `workflow` tag column** — rejected. Sharing one FTS5 index across unrelated projects pollutes BM25 ranking, complicates retention bounds (per-project caps would need partial indexes), and offers no token isolation for workflow bots. The git-sync story also collapses without a per-project file boundary.
- **One MCP server, multi-tenant, workspace selected per request** — rejected. Forces every MCP tool to take a `workspace` parameter, breaking the V1 tool surface and pushing workspace resolution into every consumer. Also blocks per-workspace token isolation: one server = one token boundary.
- **Project memory as a separate plugin, not a workspace type** — rejected. Duplicates the schema, store layer, and MCP code path. The whole appeal of the workspace abstraction is that one schema + one store layer powers all three workloads.
- **Sync repo memory via SQLite-in-git** — rejected. Binary blobs in git history bloat clones, merge conflicts on a SQLite file are unrecoverable, and `git diff` produces no useful signal for PR review. JSONL is human-readable, mergeable, and reviewable.
- **Embeddings/vector-DB based per-project memory** — rejected. Breaks the determinism wedge (see project positioning notes). FTS5 + Jaccard is reproducible across clones; embeddings tied to a specific model would drift on coworker machines without pinning.
- **Defer workflow workspaces to V2** — considered. Rejected because the user explicitly identified continuous-agent automation as a primary use case alongside repo sharing. Shipping only `user` + `project` would force workflow bots into the `user` workspace, polluting interactive memory with bot-scoped rules.

## Open questions (to resolve before implementation)

1. **Monorepo nested workspaces.** A `.droids-mem/` at both `/monorepo/` and `/monorepo/service-a/` — should resolution stop at nearest, or merge upward? Current proposal: nearest wins, no upward merge. Revisit if monorepo users hit friction.
2. **Workspace name format.** Slug-only (`email-triage-bot`) vs. path-allowed. Proposal: slug-only, registered in `~/.droids-mem/registry.yml`, with the on-disk path derived from the slug.
3. **Automatic workspace creation.** Setting `DROIDS_MEM_WORKSPACE=newname` on a workflow bot's first invocation — auto-provision with default yml, or require explicit `workspace create`? Proposal: auto with stderr notice, to keep bot startup zero-touch.
4. **Sync conflict resolution.** Two coworkers concurrently appending to `memories.jsonl` and pushing — git merge resolves cleanly if entries are sorted by ULID, but identical fingerprints from independent sessions need a tiebreaker. Proposal: ULID-sort wins, duplicates collapsed on `droids-mem sync` via existing dedupe pipeline.
5. **TUI cross-workspace search UX.** Single search bar over all known workspaces vs. workspace-scoped tabs. Proposal: scoped tabs by default, `Ctrl-G` for global.
