# droids-mem — Product Requirements Document

> **Version:** 1.3 · **Status:** Draft · **Last Updated:** June 2026
>
> **Changelog (1.3):** v1.0 scope restaged. v1.0 ships PII scrub pipeline (ADR-0007, Accepted) + `scope` column only. Workspace model (ADR-0005) deferred to v1.1. Git-jsonl sync (ADR-0006) deferred to v1.2. §9 milestones marked with target release. §11 release criteria pruned to v1.0 deliverables. See `docs/v1.0-implementation-plan.md` for locked decisions.
>
> **Changelog (1.2):** Workspace model (ADR-0005), git-jsonl sync for project workspaces (ADR-0006), PII scrub pipeline (ADR-0007), `scope` field on memories, expanded command surface (`init`, `sync`, `workspace`, `scrub`), release criteria checklist.

---

## Table of Contents

1. [Overview](#1-overview)
2. [Problem Statement](#2-problem-statement)
3. [Goals & Success Metrics](#3-goals--success-metrics)
4. [Product Shape](#4-product-shape)
5. [Core Workflows](#5-core-workflows)
6. [Data Model](#6-data-model)
7. [Memory Rules](#7-memory-rules)
8. [Local Command Surface](#8-local-command-surface)
9. [Development Milestones](#9-development-milestones)
10. [Risks & Mitigations](#10-risks--mitigations)
11. [Release Criteria](#11-release-criteria)
12. [Out of Scope](#12-out-of-scope)
13. [Glossary](#13-glossary)

---

## 1. Overview

**droids-mem** is a lightweight, local-first Go binary that gives AI agents a persistent memory layer backed by SQLite with FTS5 full-text search.

The goal of V1 is not distributed infrastructure, deployment scoping, or service orchestration. The goal is to get the **memory tool itself** right: what an agent saves, how memories are structured, how duplicate/noisy memories are avoided, and how useful context is retrieved before the next task run.

At its core, droids-mem helps an agent do three things well:

- save a useful lesson after it learns something
- search past lessons when it encounters a similar task or issue
- load a small, high-signal context snapshot at the start of a run

The binary should work fully on a local machine with zero external dependencies beyond a SQLite file.

---

## 2. Problem Statement

AI agents often repeat the same mistakes because each run starts with no persistent operational memory. Even when an agent successfully resolves an issue once, that lesson is frequently lost unless a human hardcodes the fix somewhere else.

For V1, the core problem is not networking or multitenancy. The real problem is:

**How do we give an agent a memory system that stays useful instead of turning into junk?**

That breaks into four practical challenges:

**No retained lessons.** Agents forget successful resolutions, mappings, and user corrections between runs.

**Memory noise.** If agents save everything, retrieval quality collapses. Logs, repeated summaries, and vague notes become clutter.

**Weak retrieval.** If search returns too much irrelevant text, the memory layer becomes a distraction instead of an asset.

**Unclear save behavior.** If the rules for what to save are not explicit, memory quality will vary dramatically between runs.

Droids-mem V1 solves these problems by enforcing a small set of structured memory types, a clear save policy, and fast local retrieval over SQLite + FTS5.

---

## 3. Goals & Success Metrics

### 3.1 Goals

- Build a **local-first memory binary** that works without any external services
- Define a **clear, durable memory contract** for agent-generated memories
- Keep retrieval fast enough to run at the start of every task without noticeable delay
- Keep memory human-readable and easy to inspect in SQLite
- Prevent memory quality decay through structured fields and duplicate suppression

### 3.2 Success Metrics

| Metric | Target |
|---|---|
| Local search latency (p95) | < 100ms |
| Binary startup time | < 200ms |
| Binary size (compiled) | < 20MB |
| Duplicate memory rate | ≤ 10% of saved memories are near-duplicates |
| Context payload size — always tier | 1 `last_session` (full body) + ≤ 5 `user_rules` (full body) |
| Context payload size — browse tier | ≤ 10 `error_resolution` + ≤ 10 `task_pattern` (title + 120-rune snippet each) |
| Context payload size — total ceiling | ≤ 26 items, ~34 KB at always-tier-saturated worst case |
| Storage footprint (first 90 days, single-user local usage) | < 25MB |
| Scrub redaction false-positive rate (per 1000 prose lines) | < 5 per pattern category |
| Scrub latency on 10 KB body (p95, M-series Mac) | < 500 µs |
| JSONL round-trip determinism (sync export → import → export byte-identical) | 100% |
| Workspace MCP cold-start (`ensure-server`) | < 300 ms |

---

## 4. Product Shape

### 4.1 Core Shape

Droids-mem V1 is a **single Go binary** that manages:

- a local SQLite database
- an FTS5 search index
- a small command surface for saving, searching, and loading context

The binary should be usable in any of these modes over time:

- CLI invocation
- stdio tool wrapper
- embedded/local process integration

V1 does **not** need to commit to one long-term transport layer yet. The backbone is the local memory engine itself.

### 4.2 Workspaces (ADR-0005)

A **workspace** is a named, configured memory boundary backed by exactly one SQLite database. Three workspace types share the same schema, store layer, and MCP tool surface:

| Type | Purpose | Storage | MCP lifecycle | Sync |
|------|---------|---------|---------------|------|
| `user` | Per-user interactive memory. Always-on, attached to the dev agent. Default for `mem_*` calls when no other workspace resolves. | `~/.droids-mem/local/mem.db` | One long-lived server, default `:7777`. | None. |
| `project` | Repo-scoped memory. Initialized in a repo via `droids-mem init`, committed via git, picked up by coworkers on clone. Auto-discovered via cwd walk-up. | `<repo>/.droids-mem/mem.db` (gitignored) + `<repo>/.droids-mem/memories.jsonl` (committed). | Merged into the `user` workspace's MCP at query time. No separate server. | `git-jsonl` (default). |
| `workflow` | Continuous-agent memory. Named, isolated, no repo binding. One workspace per bot. | `~/.droids-mem/workspaces/<name>/mem.db` | Dedicated server per workspace (`droids-mem serve --workspace <name>`), separate port and bearer token. | None by default; configurable. |

**Resolution order** for any `mem_*` call:

1. Explicit `--workspace <name>` flag or `DROIDS_MEM_WORKSPACE` env var
2. cwd walk-up: nearest `.droids-mem/workspace.yml` → `project` workspace
3. Fallback: `user` workspace

**Merge semantics.** When a `project` workspace is discovered alongside the `user` workspace, `mem_context` and `mem_search` return the **union** of rows, each tagged with `workspace_source`. On fingerprint collision, the `project` row wins (more specific to current work).

**Token isolation.** Each workspace owns its own bearer token (`0600` file in the workspace dir). Workflow bots cannot read `user` memory and vice versa.

Every workspace has a `workspace.yml` declaring name, type, MCP settings, sync mode, retention, scrub config, and merge rules. The yml is the source of truth; CLI flags and env vars override per-invocation.

### 4.4 Design Principles

**Single binary, zero external dependencies.** No separate database server, vector service, or background platform dependency.

**Local-first by default.** The tool should be easy to run and debug on a laptop before it is integrated into a larger runtime.

**Structured memory beats raw transcripts.** The tool stores curated lessons, not raw logs or chain-of-thought.

**Normal table + FTS index.** SQLite should use a standard `memories` table as the source of truth and an FTS5 table for search. FTS5 should not be the primary store.

**Precision over cleverness.** Retrieval should prefer small, relevant, interpretable results rather than ambitious but noisy ranking behavior.

**Readable by humans.** A human should be able to inspect the SQLite file and understand what the agent learned.

**Deterministic by design.** Same input → same output bytes. No embeddings, no LLM calls in the data path, no time-dependent behavior. Coworker clones produce identical fingerprints, identical scrubs, identical JSONL.

**Defense in depth on writes.** Every save flows through scrub → normalize → fingerprint → dedupe → insert. No bypass flags, no per-call escape hatches.

---

## 5. Core Workflows

### 5.1 Save a Memory

When an agent resolves an issue, discovers a repeatable mapping, receives a user correction, or finishes a task run, it saves a structured memory.

The save path runs a fixed pipeline (per ADR-0007):

1. validate required fields, types, enum membership
2. trim each free-text field
3. **scrub PII** on `title`, `what`, `learned`, `tags` (redacted text is what gets fingerprinted and persisted — no original retained)
4. normalize text used for deduplication
5. compute a fingerprint
6. detect likely duplicates (fingerprint + BM25/Jaccard)
7. resolve workspace (explicit → cwd walk-up → user fallback)
8. insert a new memory only if it is sufficiently distinct
9. if `project` workspace and `scope=shared`: queue JSONL export, flush before return

### 5.2 Search Memory

When an agent hits a familiar problem or starts a related task, it searches memory using concise domain-specific keywords.

Search should support:

- full-text lookup over title, what, learned, and tags
- optional filtering by `task_type`
- optional filtering by `kind`
- ranking by relevance with recency as a light tie-breaker

### 5.3 Load Start-of-Run Context

At the start of a task run, the agent should not receive a giant dump of memory.

Instead, droids-mem returns a compact context bundle using **priority slots**:

| Slot | Kind | Selection |
|---|---|---|
| 1 | `session_summary` | Latest for matching `task_type` |
| up to 3 | `error_resolution` | FTS ranked by relevance |
| up to 2 | `user_rule` | Most recent |
| up to 2 | `task_pattern` | FTS ranked by relevance |

Before returning, all candidates are deduplicated by `id`. If the same memory qualifies for multiple slots, it appears once in the highest-priority slot. Total result count is capped by the `limit` param (default 8).

The point of `context` is orientation, not exhaustive recall.

### 5.4 End a Run

At the end of every run, the agent saves one `session_summary` memory that captures:

- what the run tried to do
- what succeeded or failed
- what should be remembered next time

**Retention policy:** On each new `session_summary` save for a given `task_type`, the system counts existing summaries for that `task_type`. If count exceeds 5, the oldest by `created_at` is deleted. This bounds summary accumulation to 5 per task_type with no manual intervention.

This keeps continuity between runs without requiring full transcript replay.

### 5.5 Sync a Project Workspace (ADR-0006)

`project` workspaces sync via a single committed file: `<repo>/.droids-mem/memories.jsonl`. The local `mem.db` is gitignored and rebuildable from JSONL.

**Format.** One `MemoryRecord` per line, UTF-8 LF, alphabetical key order, sorted ascending by `id` (ULID). Deterministic byte output across machines.

**Export filter.** Only `scope=shared` rows export. `scope=personal` never leaves the local DB.

**Triggers.**
- Synchronous flush on `mem_save` to a project workspace with `sync.mode: git-jsonl`
- Explicit `droids-mem sync --export`
- `droids-mem workspace sync <name>`

Export is **always a full rewrite** (atomic temp + rename). Determinism requires it.

**Import.** `droids-mem sync --import` rebuilds the DB from JSONL through the standard dedupe pipeline. Default behavior is additive (local-only rows kept). `--prune` opts into exact-mirror semantics.

**Coworker onboarding.**

```
git clone <repo>
cd <repo>
droids-mem sync          # rebuild .droids-mem/mem.db from memories.jsonl
# agent runs; cwd walk-up resolves project workspace automatically
```

**Conflict resolution.** `.gitattributes` recommends `merge=union` for `memories.jsonl`. Standard `git merge` may produce interleaved (unsorted) output; next `droids-mem sync` re-sorts canonically. Same-ID conflicts resolve via `updated_at` tiebreak, then lexicographic `fingerprint`. Same-fingerprint, different-ID collapses via standard dedupe on import.

### 5.6 PII Scrub on Save (ADR-0007)

Every save runs through a deterministic, regex-based PII scrub before fingerprinting. Patterns cover emails, phone numbers, credit cards (Luhn-checked), SSNs, AWS/GitHub/Stripe/Slack/OpenAI/Anthropic API keys, JWTs, PEM private keys, and private IPv4 ranges. Public IPv4 and UUID patterns ship disabled (high false-positive rate).

**Workspace tuning.** `workspace.yml` supports `scrub.extra_patterns`, `scrub.disabled_patterns`, `scrub.enabled_optional_patterns`. Default patterns cannot be silently overridden — disable by name and add a new pattern with a unique name.

**Observability.** `mem_save` response includes a `scrub` block with redaction count and per-pattern counts when redactions occurred. `droids-mem doctor --scrub-stats` aggregates workspace-level trends.

**Determinism.** Pattern set + order is fixed per `scrub_pattern_version`. Two clones with the same droids-mem version produce identical scrubs.

**Migration.** `droids-mem migrate --rescrub-workspace <name>` walks every row through current scrub patterns and rewrites in place. Opt-in, irreversible, used after droids-mem upgrades that bump `scrub_pattern_version`.

---

## 6. Data Model

### 6.1 `memories` Table (Source of Truth)

```sql
CREATE TABLE memories (
    id            TEXT PRIMARY KEY,
    session_id    TEXT NOT NULL,
    task_type     TEXT NOT NULL,
    kind          TEXT NOT NULL,      -- error_resolution | task_pattern | user_rule | session_summary
    scope         TEXT NOT NULL DEFAULT 'shared'
                    CHECK (scope IN ('personal','shared')),
    title         TEXT NOT NULL,
    what          TEXT NOT NULL,
    learned       TEXT NOT NULL,
    tags          TEXT,               -- space-delimited tokens (e.g. "hubspot phone field-mapping")
    fingerprint   TEXT NOT NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);
```

`scope` partitions personal vs shared memories (ADR-0005). Default is `shared` because distilled memories (errors, patterns) are generally project-applicable. `scope=personal` rows never export to JSONL (ADR-0006).

All free-text fields (`title`, `what`, `learned`, `tags`) are stored **post-scrub** (ADR-0007). The original pre-scrub text is never persisted.

`tags` must be space-delimited, not JSON. FTS5 tokenizes on whitespace; JSON brackets/quotes break token matching.

`updated_at` is set to `created_at` on insert via an AFTER INSERT trigger. It is only mutated on forced overwrites (HITL corrections). Do not use `DEFAULT 0` — epoch timestamp corrupts recency-based tie-breaking.

This is the canonical record. All exact filtering, deduplication, timestamps, and future migrations should use this table.

### 6.2 `memories_fts` Table (Search Index)

```sql
CREATE VIRTUAL TABLE memories_fts USING fts5(
    title,
    what,
    learned,
    tags,
    content='memories',
    content_rowid='rowid'
);
```

This follows the SQLite **External Content Table** pattern (see SQLite FTS5 docs, "External Content Tables"). The FTS index does not auto-sync — three triggers are required at schema init:

```sql
-- keep FTS in sync with memories table
CREATE TRIGGER memories_ai AFTER INSERT ON memories BEGIN
  INSERT INTO memories_fts(rowid, title, what, learned, tags)
  VALUES (new.rowid, new.title, new.what, new.learned, new.tags);
END;

CREATE TRIGGER memories_ad AFTER DELETE ON memories BEGIN
  INSERT INTO memories_fts(memories_fts, rowid, title, what, learned, tags)
  VALUES ('delete', old.rowid, old.title, old.what, old.learned, old.tags);
END;

CREATE TRIGGER memories_au AFTER UPDATE ON memories BEGIN
  INSERT INTO memories_fts(memories_fts, rowid, title, what, learned, tags)
  VALUES ('delete', old.rowid, old.title, old.what, old.learned, old.tags);
  INSERT INTO memories_fts(rowid, title, what, learned, tags)
  VALUES (new.rowid, new.title, new.what, new.learned, new.tags);
END;
```

FTS queries return `rowid` (SQLite internal integer). Bridge to the `memories` table via:

```sql
SELECT m.* FROM memories m
WHERE m.rowid IN (
  SELECT rowid FROM memories_fts WHERE memories_fts MATCH ?
)
ORDER BY rank;
```

Command responses always expose the TEXT `id` field — callers never see or need `rowid`.

### 6.3 Memory Kinds

| Kind | When Used | Example |
|---|---|---|
| `error_resolution` | Agent encountered and fixed an error | API rejected `phone_number`; use `phone` |
| `task_pattern` | Agent discovered a repeatable successful pattern | CSV import works when dates are normalized to ISO-8601 |
| `user_rule` | User correction or stable preference should persist | Always abbreviate Company as Co. |
| `session_summary` | End-of-run summary | Import completed; blank company rows still need review |

### 6.4 Optional Future Fields

These are intentionally deferred from V1 unless they become necessary during implementation:

- `why`
- `where_`
- `source_tool`
- `confidence`
- `expires_at`
- `supersedes_id`

V1 should stay small unless real usage proves these fields are needed.

### 6.5 Fingerprint Strategy

Each memory gets a deterministic `fingerprint` derived from normalized fields. V1 uses a two-layer dedupe approach:

**Layer 1 — Exact fingerprint match**

Normalization pipeline (applied to `title` + `learned` before hashing):

1. lowercase
2. trim whitespace
3. collapse internal whitespace to single space
4. strip punctuation
5. sort words alphabetically
6. concatenate with `task_type` and `kind`
7. SHA-256 → hex string

A memory with identical fingerprint is skipped or force-overwritten (see §8.1).

**Layer 2 — Pre-save BM25 check**

Before inserting, run an FTS search using the new memory's `title` + `learned` as the query. If the top result has a BM25 rank above a defined threshold, treat the new memory as a near-duplicate and skip insertion. This catches paraphrased lessons that produce different fingerprints but carry the same meaning.

Fuzzy duplicate detection (SimHash, shingle similarity) is deferred to post-V1.

### 6.6 Workspace Storage Layout (ADR-0005)

```
~/.droids-mem/
  local/                          # user workspace
    workspace.yml
    mem.db
    token
    mcp.pid
    mcp.log
  workspaces/                     # workflow workspaces
    <name>/
      workspace.yml
      mem.db
      token
      mcp.pid
      mcp.log
  registry.yml                    # known workspaces, default selection

<repo>/.droids-mem/                # project workspace
  workspace.yml                   # committed
  memories.jsonl                  # committed (scope=shared rows only)
  mem.db                          # gitignored
  token                           # gitignored
  mcp.pid                         # gitignored
  mcp.log                         # gitignored
  .gitignore                      # auto-generated by `droids-mem init`
```

`droids-mem init` in a repo seeds `.gitignore` with `mem.db token mcp.pid mcp.log` and writes `.gitattributes` line `.droids-mem/memories.jsonl merge=union` (interactive prompt; `--yes` skips).

### 6.7 `workspace.yml` Schema (V1)

```yaml
name: <slug>                       # required; matches registry key for user/workflow types
type: user | project | workflow    # required
mcp:
  addr: ":7777"                    # user defaults :7777; workflow auto-assigns per registry
  endpoint: /mcp
  token_file: ./token
sync:
  mode: none | git-jsonl           # project default git-jsonl; user/workflow default none
  export_scope: shared             # only scope=shared rows exported
  jsonl_path: ./memories.jsonl     # relative to workspace dir
retention:
  session_summary_per_task_type: 5 # V1 cap; other kinds unbounded
auto_discover_repos: true          # user workspace only; enables cwd walk-up merge
merge_into_local: true             # project workspace; allows user MCP to read this workspace
scrub:
  enabled: true
  scrub_pattern_version: 1
  extra_patterns: []
  disabled_patterns: []
  enabled_optional_patterns: []
```

Workspace load fails fast on unknown fields, invalid regex in `extra_patterns`, duplicate pattern names, or schema_version mismatch.

---

## 7. Memory Rules

### 7.1 Always Save

The agent should always save:

- an error it successfully resolved
- a repeatable transformation or mapping learned for the first time
- a user correction or user-specific rule
- one `session_summary` at the end of the run

### 7.2 Never Save

The agent should never save:

- raw tool call logs
- intermediate reasoning or chain-of-thought
- full transcripts
- giant unstructured blobs of text
- duplicate memories with no new lesson
- sensitive raw data when a generalized lesson can be stored instead

### 7.3 Required Quality Bar

A valid memory should answer two questions clearly:

- **What happened?**
- **What should the agent do next time?**

If a memory does not teach a reusable lesson, it should not be saved.

### 7.4 Duplicate Handling

Duplicate control is a core part of V1.

When a new memory is saved, droids-mem should:

1. compute the fingerprint
2. check for an existing memory with the same fingerprint
3. either skip the write, or refresh metadata if the project later chooses to support that behavior

V1 may start with exact fingerprint dedupe only. Fuzzy duplicate detection can wait.

### 7.5 Scope Selection (ADR-0005)

`scope` defaults to `shared`. Override to `personal` when a memory is genuinely tied to the individual user, not the project:

| Likely `shared` | Likely `personal` |
|-----------------|-------------------|
| API field-mapping errors | "I prefer tabs over spaces in my own work" |
| Build / test command corrections for this repo | Editor / shell aliases |
| Domain rules from this product | Personal commit-message style |
| Patterns that solve recurring repo bugs | Local machine paths / env quirks |

When in doubt, leave `scope=shared`. PII scrub (ADR-0007) is the safety net; scope is the editorial decision about who else benefits.

---

## 8. Local Command Surface

The binary should expose a small local command surface. The exact transport may evolve, but the operations should be stable.

### 8.1 `save`

Stores one structured memory.

**Input shape**

```json
{
  "session_id": "sess_001",
  "task_type": "crm_upload",
  "kind": "error_resolution",
  "title": "HubSpot phone field mapping",
  "what": "Upload failed because the target field was phone_number",
  "learned": "Map Phone Number to phone",
  "tags": ["hubspot", "field-mapping", "phone"]
}
```

**Optional field**

| Field | Type | Description |
|---|---|---|
| `force` | bool | If `true`, overwrite existing memory with same fingerprint instead of skipping |

`force` is the HITL correction path. When a human corrects agent behavior, the agent resaves the memory with `force: true`. The system finds the existing record by fingerprint, updates `title`, `what`, `learned`, `tags`, and `updated_at` in place, and syncs the FTS index via the update trigger.

**Expected behavior**

- validate required fields
- normalize text
- compute fingerprint (layer 1 dedupe)
- run pre-save BM25 check (layer 2 dedupe)
- if `force=false` (default): skip if fingerprint or BM25 threshold matched
- if `force=true`: overwrite matched record; insert if no match found
- return saved or updated memory metadata

**Response shapes**

Successful save:
```json
{ "status": "saved", "id": "mem_01J...", "session_id": "sess_01J..." }
```

Duplicate skipped (layer 1 — exact fingerprint):
```json
{ "status": "skipped", "reason": "duplicate", "matched_id": "mem_01J..." }
```

Near-duplicate skipped (layer 2 — BM25):
```json
{ "status": "skipped", "reason": "near_duplicate", "matched_id": "mem_01J...", "score": -18.4 }
```

Force overwrite applied:
```json
{ "status": "updated", "id": "mem_01J...", "session_id": "sess_01J..." }
```

Validation failure:
```json
{ "status": "error", "code": "validation_failed", "field": "kind", "message": "kind must be one of: error_resolution, task_pattern, user_rule, session_summary" }
```

**Session ID handling**

`session_id` is optional on input. If omitted, the binary generates a ULID and returns it in the response. The agent should reuse the returned `session_id` for all subsequent saves in the same run to group memories under one session.

### 8.2 `search`

Searches stored memories.

**Input shape**

```json
{
  "query": "hubspot phone mapping",
  "task_type": "crm_upload",
  "kind": "error_resolution",
  "limit": 5
}
```

**Expected behavior**

- search FTS index
- filter on exact columns from `memories`
- return concise results with title, learned, kind, and created_at

### 8.3 `context`

Returns a compact set of memories to prime the next run.

**Input shape**

```json
{
  "task_type": "crm_upload",
  "query": "hubspot phone field mapping",
  "limit": 8
}
```

`query` is optional. If omitted, the binary tokenizes `task_type` and uses those tokens as the FTS query for relevance ranking. If provided, `query` is used directly for FTS slots (`error_resolution`, `task_pattern`). `user_rule` and `session_summary` slots use recency only — no FTS ranking applied.

**Expected behavior**

- fetch latest `session_summary` for `task_type` (recency)
- fetch top `error_resolution` matches via FTS on `query`
- fetch most recent `user_rule` memories
- fetch top `task_pattern` matches via FTS on `query`
- deduplicate by `id` across all slots
- trim to `limit`

Return a bundle shaped like:

```json
{
  "last_session": {
    "id": "mem_01J...",
    "title": "...",
    "learned": "...",
    "created_at": 1234567890
  },
  "memories": [
    { "kind": "error_resolution", "id": "mem_01J...", "title": "...", "learned": "...", "created_at": 1234567890 },
    { "kind": "user_rule",        "id": "mem_01J...", "title": "...", "learned": "...", "created_at": 1234567890 },
    { "kind": "task_pattern",     "id": "mem_01J...", "title": "...", "learned": "...", "created_at": 1234567890 }
  ]
}
```

### 8.4 `inspect` (Optional but Helpful)

A lightweight local inspection command is useful for debugging and trust.

Possible examples:

- list recent memories
- show one memory by ID
- print latest session summaries

This is optional for V1 but strongly recommended during development.

### 8.5 `init`

Initializes a `project` workspace in the current directory (or `--repo <path>`).

**Behavior:**
- Errors if `.droids-mem/` already exists (use `--force` to recreate).
- Writes `.droids-mem/workspace.yml` (`type: project`).
- Appends `.gitignore` entries: `mem.db`, `token`, `mcp.pid`, `mcp.log`.
- Writes `.gitattributes` line `.droids-mem/memories.jsonl merge=union` (interactive prompt; `--yes` skips).
- Creates empty `memories.jsonl` placeholder.
- Registers workspace in `~/.droids-mem/registry.yml`.

### 8.6 `sync`

Reconciles a `project` workspace between SQLite and JSONL (ADR-0006).

**Flags:**
- `--import` — JSONL → DB only.
- `--export` — DB → JSONL only.
- `--prune` — when importing, drop local-only rows so DB matches JSONL exactly.
- `--dry-run` — print diff, no writes; exit `10` on success.
- `--workspace <name>` — explicit workspace override.

Default (no flags): `--import` then `--export`. Round-trip safe.

### 8.7 `workspace`

Workspace registry operations.

**Subcommands:**
- `create <name> --type workflow` — provision a workflow workspace.
- `list` — JSON list of registered workspaces.
- `remove <name>` — remove registry entry (does not delete data unless `--purge`).
- `status [<name>]` — health summary (DB rows, last save, MCP pid, port).
- `sync <name>` — alias for `sync --workspace <name>`.

### 8.8 `scrub`

PII scrub utilities (ADR-0007).

**Flags:**
- `--check <file>` — run scrub against arbitrary text, print `ScrubReport`. No DB write.
- `--test` — run fixture corpus tests (`internal/store/testdata/scrub/`), print pass/fail.

### 8.9 `migrate`

Schema and content migrations across droids-mem versions.

**Subcommands:**
- `migrate --rescrub-workspace <name>` — walk every row, apply current scrub patterns, rewrite in place. Reports per-row diffs to stderr. Opt-in, irreversible.
- `migrate --to-workspace <name> --filter <expr>` — move subset of rows from one workspace to another (e.g., split user memory into a new project workspace).

---

## 9. Development Milestones

| Phase | Target | Deliverable | Success Criteria |
|---|---|---|---|
| **M1** | shipped | SQLite schema | `memories` table and `memories_fts` index created successfully |
| **M2** | shipped | `save` implementation | Structured memories save locally with validation |
| **M3** | shipped | Fingerprint dedupe | Exact duplicates are skipped reliably |
| **M4** | shipped | `search` implementation | Relevant FTS results returned with exact filters |
| **M5** | shipped | `context` implementation | Latest session summary + relevant memories returned in compact form |
| **M6** | shipped | Local CLI or stdio wrapper | Tool can be exercised end-to-end on a laptop |
| **M7** | shipped | End-to-end memory loop | A second run uses a first run's memories successfully |
| **M8** | **v1.1** | Workspace model (ADR-0005) | `user`/`project`/`workflow` types load from `workspace.yml`; resolution order works; merge-on-query returns tagged union |
| **M9** | **v1.0** | PII scrub pipeline (ADR-0007) | Pattern set lands; fixture corpus passes; redaction reports in `mem_save` response; benchmark p95 < 500 µs |
| **M9a** | **v1.0** | `scope` column + schema migration | `PRAGMA user_version` ladder; v0→v1 adds `scope`, `scrub_pattern_version`, `scrub_counts`, `meta` kv; boot gate requires `scrub_baseline_complete` sentinel |
| **M9b** | **v1.0** | `migrate --rescrub` / `--no-rescrub` | Pre-v1.0 DB boot fails → `migrate` produces ready DB; atomic per DB |
| **M9c** | **v1.0** | `scrub --check` + `doctor --scrub-stats` | Human-facing CLI for scrub debugging + operator aggregation |
| **M10** | **v1.2** | Project workspace sync (ADR-0006) | `init` + `sync` work end-to-end; JSONL byte-deterministic; coworker clone + sync produces identical DB |
| **M11** | **v1.0** | Release packaging | GitHub releases (linux/amd64, linux/arm64, darwin/arm64, darwin/amd64), `go install` path, README + quick-start, CHANGELOG, license |

---

## 10. Risks & Mitigations

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Agents save vague or useless memories | Medium | High | Enforce a strict schema and quality rules for `what` and `learned` |
| Duplicate memories flood the database | High | High | Add fingerprint-based dedupe from day one |
| Search returns noisy results | Medium | Medium | Keep memory fields small and structured; limit `context` payload size |
| Session summaries become repetitive clutter | Medium | Low | Only keep one summary per run and favor latest-per-task in `context` |
| FTS query behavior is confusing on short inputs | Medium | Medium | Prefer domain-specific queries and test common search phrases early |
| Schema is over-designed too early | Medium | Medium | Keep V1 fields minimal and defer optional metadata |
| PII leaks into committed `memories.jsonl` | High | Critical | ADR-0007 scrub pipeline runs before fingerprint on every save; redaction counts in `mem_save` response; `doctor --scrub-stats` surfaces undetected leak rates |
| Scope-misuse: `scope=shared` on personal memory leaks to repo | Medium | High | Default `shared` covers distilled memories; CLI guidance + TUI badges flag scope; `migrate --to-workspace` recovery path |
| Workspace `enabled: false` scrub disabled in error | Low | Critical | Loud WARN every save + startup notice; disabling for `project` workspaces with `sync.mode: git-jsonl` rejected at load time |
| JSONL merge conflicts on concurrent coworker writes | Medium | Medium | `.gitattributes merge=union` + deterministic sort + dedupe on next sync; `updated_at` tiebreak rule documented |
| Cross-workspace token leak (workflow bot reads user memory) | Low | High | Per-workspace bearer tokens (`0600`), separate MCP processes for workflow type; no shared keyring |
| False-positive scrubs break legitimate examples | Medium | Medium | Per-workspace `disabled_patterns` + `extra_patterns`; CLI `scrub --check` for debugging |
| Scrub-pattern-version drift across coworker clones | Medium | Medium | `scrub_pattern_version` recorded per row; `sync --import` re-scrubs with current patterns and re-exports |

---

## 11. Release Criteria

Pre-release blockers before tagging v1.0.0. v1.0 scope = scrub pipeline + `scope` column + schema migration mechanic. Workspace model (M8) and JSONL sync (M10) deferred to v1.1 / v1.2 respectively. See `docs/v1.0-implementation-plan.md` for locked decisions.

**Correctness**
- [ ] All existing tests pass on `main` (currently one broken test flagged in last commit — fix or quarantine).
- [ ] M9 + M9a + M9b + M9c implemented with end-to-end test coverage.
- [ ] Schema migration v0 → v1 idempotent; fresh-DDL schema byte-equivalent to ALTER-migrated schema.
- [ ] Scrub fixture corpus (`internal/store/testdata/scrub/corpus.yaml`) passes.
- [ ] Scrub benchmark p95 < 500 µs on 10 KB body.
- [ ] E2E: pre-v1.0 fixture DB → boot fails → `migrate --rescrub` succeeds → boot succeeds.

**Safety**
- [ ] PII scrub pipeline (ADR-0007) live in save path; pattern set committed in declaration order (pem_key first → email last).
- [ ] Boot gate refuses to start without `scrub_baseline_complete` sentinel in `meta` table.
- [ ] Empty-after-scrub on `learned` rejected (`scrub_emptied_learned`).
- [ ] Tag-with-secret rejected (`tag_contains_secret`, `retryable:true`).
- [ ] Field caps enforced (title=200, what=8192, learned=4096, tags=500).
- [ ] Redaction tokens bracketed per-category ([EMAIL], [AWS_KEY], …).

**Packaging**
- [ ] GitHub Actions release builds: `linux/amd64`, `linux/arm64`, `darwin/arm64`, `darwin/amd64`.
- [ ] `go install github.com/samuelmolero/droids-mem/cmd/droids-mem@latest` works.
- [ ] Binary < 20 MB (per §3.2).
- [ ] LICENSE file committed (recommended: MIT or Apache-2.0).

**Documentation**
- [ ] `README.md` with: install, quick start (save/search/context), scrub behavior, first-run `migrate --rescrub`, troubleshooting.
- [ ] `CHANGELOG.md` seeded with v1.0.0 entry.
- [ ] ADR 0001-0007 committed and linked from `README.md`. ADR-0005/0006 marked Deferred. ADR-0007 marked Accepted.
- [ ] `droids-mem doctor --scrub-stats` documented for operators.

**Migration**
- [ ] `droids-mem migrate --rescrub` walks rows through scrub pipeline atomically; sets `scrub_baseline_complete=1`.
- [ ] `droids-mem migrate --no-rescrub` sets sentinel without rewriting (documented escape hatch for trusted DBs).

**Out of v1.0 release criteria** (deferred to v1.1 / v1.2):
- ~~Workspace model end-to-end test coverage~~ → v1.1
- ~~JSONL round-trip determinism test~~ → v1.2
- ~~`enabled: false` on `project` workspace rejected~~ → v1.1
- ~~Per-workspace token isolation~~ → v1.1
- ~~`migrate --rescrub-workspace`~~ → v1.2 (v1.0 ships single-DB `migrate --rescrub`)

---

## 12. Out of Scope

The following are explicitly out of scope for droids-mem V1:

- HTTP service deployment beyond local MCP server
- authentication beyond per-workspace bearer tokens (no OIDC, no RBAC)
- cloud sync or cross-machine sync outside git transport
- vector search / embeddings
- web UI / admin console
- branch-scoped memory (project workspace memory is per-repo, not per-branch)
- memory editing workflows beyond basic duplicate suppression and `migrate`
- automated capture of raw tool logs or model output
- soft-delete / undelete (V1 has no `deleted_at` column; deletes are hard)
- multi-writer concurrent saves to the same workspace (single-writer per ADR-0004)

---

## 13. Glossary

| Term | Definition |
|---|---|
| **Memory** | A structured lesson saved for future agent runs |
| **FTS5** | SQLite's built-in full-text search engine |
| **Fingerprint** | A deterministic hash used to suppress duplicate memories |
| **`error_resolution`** | A memory describing a problem and the fix that worked |
| **`task_pattern`** | A repeatable successful pattern worth reusing |
| **`user_rule`** | A user correction or stable preference the agent should remember |
| **`session_summary`** | A run-level summary saved at the end of a task |
| **Context** | A compact set of memories returned at the start of a run |
| **Workspace** | A named, configured memory boundary backed by exactly one SQLite DB. Three types: `user`, `project`, `workflow` |
| **Scope** | Per-memory field: `personal` (never exported) or `shared` (default; exports to JSONL in project workspaces) |
| **JSONL sync** | Git-native export format for project workspaces: one MemoryRecord per line, sorted by ULID, deterministic byte output |
| **Scrub** | Deterministic regex-based PII redaction applied to every save before fingerprinting |
| **ScrubReport** | Per-call summary of redactions: pattern names and counts, surfaced in `mem_save` response |
| **Registry** | `~/.droids-mem/registry.yml` — known workspaces, default selection, port assignments |

---
