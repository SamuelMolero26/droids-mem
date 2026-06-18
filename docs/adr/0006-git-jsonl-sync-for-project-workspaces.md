# 0006 — Git-native JSONL sync for `project` workspaces

**Status**: Deferred to v1.2
**Date**: 2026-06-08
**Last Updated**: 2026-06-08
**Depends on**: ADR 0005 (three-layer workspace model)

> **v1.0 amendment (2026-06-08):** Implementation deferred to v1.2 per [v1.0 implementation plan](../v1.0-implementation-plan.md). Depends on workspace model (ADR-0005) shipping in v1.1 first. `scope` column lands in v1.0 schema as forward-compat so no schema migration needed when this ADR ships.

## Context

ADR 0005 introduces the `project` workspace type: a per-repo memory boundary that travels with the codebase so coworkers cloning the repo inherit the agent's accumulated learnings. That ADR names `git-jsonl` as the default sync mode but defers the concrete format, mechanics, conflict semantics, and lifecycle to this ADR.

The constraints driving the design:

1. **Reviewable in PR.** Agent-saved memories must appear as human-readable diffs so a maintainer can reject bad learnings before merge.
2. **Mergeable.** Two coworkers working on parallel branches must be able to merge their respective `memories.jsonl` files without a binary-conflict dead-end.
3. **Deterministic.** Same input set → same on-disk bytes, regardless of insertion order, agent, or machine. Without determinism, every agent run produces a no-op diff that pollutes git history.
4. **Round-trippable.** `jsonl → db → jsonl` must produce the original file (modulo dedupe). The DB is a cache; the JSONL is the source of truth.
5. **Local-first.** No network calls, no external services. Sync is a local file operation; git is the transport, not a dependency of the sync logic.
6. **Schema-compatible with single-store V1.** No new memory fields beyond those already defined in ADR 0005 (`scope`). Existing rows export cleanly.
7. **Honors ADR 0004 broker contract.** Sync writes do not introduce a new writer; they materialize existing writes from the DB.

SQLite-in-git was rejected in ADR 0005 (binary diffs, unrecoverable merge conflicts, no PR review value). JSON arrays were considered and rejected (one-line-per-row diffs are easier to review and merge than nested array reformats). YAML was rejected (whitespace-sensitive diffs, ambiguous typing, slower parse).

## Decision

`project` workspaces sync via a single committed file: `<repo>/.droids-mem/memories.jsonl`. The local `mem.db` is gitignored and treated as a rebuild-on-demand cache.

### File format

One JSON object per line, UTF-8, LF line endings, terminating LF. No leading BOM. No blank lines. No comments.

Each line is a `MemoryRecord`:

```json
{"id":"01HXYZ...","kind":"error_resolution","task_type":"go","scope":"shared","title":"FTS5 panics on unbalanced quote","what":"...","learned":"...","tags":"sqlite fts5 panic","fingerprint":"sha256:abc123...","created_at":1717804800,"updated_at":1717804800,"schema_version":1}
```

**Field rules:**

- `id` — ULID, ASCII, 26 chars. Monotonic per workspace, time-ordered.
- `kind` — one of `session_summary`, `task_pattern`, `error_resolution`, `user_rule` (ADR 0004 enum, unchanged).
- `scope` — `shared` only. `scope=personal` rows never appear in the JSONL (see "Export filter" below).
- `task_type` — required, lowercase slug.
- `title` / `what` / `learned` / `tags` — strings, normalized via existing `scrub.go` rules before export. Newlines preserved as `\n` in JSON escaping.
- `fingerprint` — full `sha256:<hex>` form. Used as the canonical dedupe key on import.
- `created_at` / `updated_at` — Unix epoch seconds, integer. `updated_at >= created_at` invariant from V1 schema preserved.
- `schema_version` — integer, currently `1`. Bump on any breaking change to record shape.
- Field order in serialized JSON is fixed (alphabetical by key) to guarantee deterministic byte output across machines and Go versions.

**Line order is fixed: ascending by `id` (ULID).** ULIDs are time-ordered, so this also produces chronological-ish order in the file, which makes `git blame` and PR review intuitive. Sort is byte-wise on the ULID string.

### Export filter

A row is exported to JSONL iff all hold:

- `scope = 'shared'`
- `kind` is one of the four V1 kinds
- Row has not been soft-deleted (V1 has no soft-delete; reserved for future)

`scope=personal` rows stay in the local DB forever. The export pipeline never reads them.

### Export trigger

Export runs in three cases:

1. **On `scope=shared` save** to a `project` workspace via the MCP `mem_save` tool. The export is queued and flushed before the tool call returns, so the JSONL on disk is always consistent with the DB at end-of-call. Flush failure aborts the save with a clear error; no partial state.
2. **On explicit `droids-mem sync --export`** for the resolved project workspace.
3. **On `droids-mem workspace sync <name>`** for the named workspace.

Export is **always a full rewrite** of `memories.jsonl`, not an append. Determinism requires it: incremental append would diverge from the canonical sort order the moment a row is updated. The write is atomic via temp-file + `rename(2)` — readers never observe a partial file.

### Import / rebuild

`droids-mem sync` (or `sync --import`) reconstructs the DB from JSONL:

1. Open `memories.jsonl`. Validate each line parses as `MemoryRecord` v1.
2. For each record, run the existing two-layer dedupe pipeline (fingerprint, then BM25+Jaccard) inside one `BEGIN IMMEDIATE` transaction.
3. Rows already in the DB with matching `fingerprint` are no-ops. Rows missing locally are inserted with their original `id`, `created_at`, `updated_at`.
4. Rows present in DB but absent in JSONL are **kept** by default. JSONL is additive to the local DB, not a replace operation. `--prune` flag opts into "make DB match JSONL exactly" semantics for forensic rebuilds.

`droids-mem sync` without flags = `--import` followed by `--export`. Round-trip safe: if the JSONL is canonical and the DB matches, output bytes equal input bytes.

### Coworker onboarding flow

```
git clone <repo>
cd <repo>
droids-mem init --discover   # registers workspace, no overwrite if .droids-mem/ exists
droids-mem sync              # builds .droids-mem/mem.db from memories.jsonl
# agent runs, cwd walk-up resolves project workspace, queries hit local DB
```

`droids-mem sync` is also wired into a default `post-merge` git hook when `droids-mem init` runs in a repo. The hook is opt-in (printed instructions, not auto-installed) to avoid surprising users about repository git config changes.

### Conflict resolution on merge

Two coworkers append memories on parallel branches. On `git merge`:

1. If both sides added distinct lines (different `id`), the standard git merge produces an interleaved file. The interleaving is wrong (not sorted by `id`), but `droids-mem sync` re-sorts on next import and re-exports canonically. Net: one extra commit cleans the merge.
2. If both sides modified the same line (same `id`, different content), git marks a conflict. Resolution rule: take the side with the higher `updated_at`. If tied, take the side with the lexicographically larger `fingerprint`. Both rules are documented and applied by `droids-mem merge --resolve` for users who don't want to hand-edit.
3. If both sides added rows with the same `fingerprint` but different `id` (same memory learned independently in two clones), the existing dedupe layer collapses them on import: the older `id` wins, the newer is dropped. The merged JSONL is canonical after `droids-mem sync`.
4. **`.gitattributes` recommendation.** `droids-mem init` writes:
   ```
   .droids-mem/memories.jsonl merge=union
   ```
   `merge=union` tells git to keep both sides of conflicting hunks. Combined with deterministic sort + dedupe on next sync, this resolves the vast majority of real-world conflicts without manual edits.

### What is *not* synced

- `mem.db` — gitignored.
- `token` — gitignored. Each clone has its own bearer token.
- `mcp.pid` / `mcp.log` — gitignored.
- `scope=personal` rows — never exported.
- FTS5 indexes — rebuilt locally from `memories` table on import.
- Workspace config (`workspace.yml`) — committed, but covered by ADR 0005, not this ADR.

### CLI surface

```
droids-mem sync                     # import + export, default for the resolved workspace
droids-mem sync --import            # JSONL → DB only
droids-mem sync --export            # DB → JSONL only
droids-mem sync --import --prune    # DB ← JSONL exact mirror, drops local-only rows
droids-mem sync --dry-run           # show diff (rows that would be added/removed), no writes
droids-mem merge --resolve <file>   # resolve conflict markers in memories.jsonl
```

Exit codes follow the existing CLI contract: `0` ok, `1` runtime, `2` usage, `5` conflict, `10` dry-run pass.

## Consequences

**Accepted**

- Project memory travels with the repo. Coworker workflow is `clone → sync → work`. Zero shared infrastructure, zero coordination outside git.
- Every agent-learned memory is reviewable as a one-line JSON diff in PR. Maintainers can reject bad learnings before merge.
- Deterministic byte output means clean agent runs that touch existing memories produce no spurious diffs. Git history stays signal-rich.
- The `memories.jsonl` file is a durable, portable archive independent of SQLite. A future schema migration can re-import historical JSONL even if the SQLite format changes.
- Sync logic is pure file I/O. No network, no external services, fully local-first. Works on airgapped machines.
- All ADR 0004 invariants hold: Root remains sole writer; the JSONL export is downstream of `mem_save`, not a parallel write path.

**Tradeoffs**

- Full-file rewrite on every `scope=shared` save costs O(N) writes, where N is the count of shared rows in the workspace. At ~200 bytes per row average, a 10K-row workspace rewrites ~2 MB per save. Acceptable for V1 (atomic rename, SSD-fast, well under MCP roundtrip cost). Revisit with chunked-file format if a workspace exceeds 100K rows.
- The JSONL file is the source of truth, but the DB is what queries hit. A skew window exists between save and export. Bounded: the synchronous flush on `mem_save` keeps the window to one tool call. Crashes mid-flush are recoverable from the DB on next `sync --export`.
- `merge=union` plus dedupe handles most conflicts, but two coworkers editing the same memory on parallel branches will lose one edit. Mitigated by `updated_at` tiebreak rule, but not eliminated. Documented as expected behavior.
- Soft-delete is not in V1, so removing a memory from the workspace requires manual `memories.jsonl` editing. Adding soft-delete in a future schema bump (`schema_version: 2`) is the clean path; ad-hoc deletions are out of scope here.
- Personal-vs-shared scope is a user judgment call. A user who saves everything as `shared` will leak personal preferences into the repo. Mitigated by the `scope=shared` default being the right call for distilled memories (errors, patterns) but the wrong call for raw preferences. CLI guidance + TUI badges flag this; no enforcement at the data layer.
- `.gitattributes` modification on `droids-mem init` touches repo config. Init prints what it will write and asks before doing so when run interactively; non-interactive (`--yes`) skips the prompt.

## Alternatives considered

- **Per-row file, one JSON per filename in `.droids-mem/rows/<ulid>.json`** — rejected. Better merge granularity but explodes inode usage, kills `git log` readability, and makes the "review the agent's recent learnings" PR workflow tedious (N files instead of one diff).
- **Append-only JSONL, no canonical sort** — rejected. Loses determinism. Every clean agent run that revisits an existing memory would still rewrite trailing portions of the file when the row gets reordered, producing noise diffs.
- **Single JSON array file** — rejected. Reformatting on insert produces large diffs that obscure the one-row change. Merge tools handle one-line-per-record far better than nested arrays.
- **SQLite-in-git with `git lfs`** — rejected. Binary blob in history, unrecoverable merge conflicts, no PR review value, LFS adds an external service dependency (violates local-first).
- **`.sql` dump file** — rejected. Larger than JSONL, harder to parse for non-SQLite tooling, and DDL noise dominates the diff.
- **CRDT-based merge (e.g., automerge)** — rejected for V1. Solves concurrent-edit semantics correctly but adds a dependency, increases file size, and is invisible in PR review. Revisit if real-world conflict frequency justifies the complexity.
- **Sync via a third-party transport (S3, Dropbox, custom server)** — rejected. Violates local-first and adds an operational dependency the user explicitly does not want.
- **Export embeddings alongside memories** — rejected. Embeddings are non-deterministic across model versions, bloat the file, and would break the "FTS5-only, no ML deps" determinism wedge.

## Open questions (to resolve before implementation)

1. **JSONL versioning strategy.** When `schema_version` bumps to `2`, do older clients refuse to import or skip unknown fields with a warning? Proposal: skip-with-warn for additive changes, hard-fail for incompatible changes, with a migration command (`droids-mem migrate --jsonl`) to upgrade in place.
2. **Hook installation default.** Should `droids-mem init` write the `post-merge` git hook by default, or only on `--install-hook`? Proposal: print instructions only, install on explicit flag. Touching `.git/hooks/` without consent is too invasive.
3. **Large file threshold.** At what row count do we switch from single-file to sharded format (e.g., `memories-<bucket>.jsonl`)? Proposal: defer; instrument size in `doctor` and revisit when a real workspace approaches the threshold.
4. **PR-review tooling.** Should the project ship a CLI command (`droids-mem review <PR-diff>`) that pretty-prints proposed memory changes for reviewers? Useful but not blocking V1 of sync; defer to a follow-up.
5. **Branch-scoped memory.** Should memories learned on a feature branch be scoped to that branch? Currently no — JSONL is per-repo, per-workspace. Branch scoping would require either branch-name in row metadata or per-branch JSONL files. Proposal: out of scope for V1; revisit if branch divergence causes friction.
