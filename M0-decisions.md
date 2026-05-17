# droids-mem — M0 Pre-Implementation Decisions

> Lock these before writing any Go code. Every item here is a fork that affects all downstream milestones.

---

## 1. Transport

**Decision: CLI (subcommands)**

`droids-mem` is the noun. Subcommands are verbs. Follows CLI-GUIDE §1 noun-verb principle.

```
droids-mem save    --task-type crm_upload --kind error_resolution --title "..." --what "..." --learned "..." --tags "hubspot phone"
droids-mem search  --query "hubspot phone" --task-type crm_upload --limit 5
droids-mem context --task-type crm_upload --query "hubspot phone"
droids-mem list    [--task-type crm_upload] [--kind error_resolution] [--limit 20]
droids-mem get     --id mem_01J...
droids-mem doctor
droids-mem schema  [save|search|context|list|get|doctor]
```

`context` does not accept `--limit`. The response is a two-tier bundle —
always tier (latest session_summary + ALL user_rules, full body) and
browse tier (top errors/patterns as title+snippet). See ADR 0002.

`doctor` runs FTS integrity-check, rebuild-if-divergent, optimize, and
VACUUM. Safe to invoke at any time; reports JSON.

`inspect` is dropped — replaced with `list` (recent memories) and `get` (single by ID). Per CLI-GUIDE §1: no catch-all subcommands. Each verb is discrete.

`schema` added for agent introspection. Returns command parameter definitions as JSON. Per CLI-GUIDE §7.

All output is JSON to stdout. Errors are JSON to stderr with non-zero exit code.

**TTY-aware behavior (CLI-GUIDE §4)**
- Non-TTY (pipe/agent): JSON output, no colors, no prompts — automatic
- TTY (terminal): table output allowed, colors allowed
- `--no-interactive` forces non-TTY mode explicitly
- `NO_COLOR` and `TERM=dumb` env vars respected
- Never hang waiting for input in non-TTY mode — fail immediately

**Dry-run (CLI-GUIDE §5)**
- `save` supports `--dry-run`
- Returns structured JSON of what would be inserted/skipped/updated — no write occurs
- Exit code `10` on dry-run pass

---

## 2. SQLite Library

**Decision: `modernc.org/sqlite`**

- Pure Go, no CGO
- FTS5 supported
- Used in production by engram
- Easier cross-compile, smaller build surface
- Verified: `import sqlite "modernc.org/sqlite"` + `_ "modernc.org/sqlite"` in main

---

## 3. DB File Location

**Decision: env var with default fallback**

```
DROIDS_MEM_DB=~/.droids-mem/mem.db   (default)
```

Binary resolves path at startup:
1. Read `DROIDS_MEM_DB` env var
2. If empty, use `$HOME/.droids-mem/mem.db`
3. Create directory if not exists (`os.MkdirAll`)
4. Open or create DB file

Set env in the `droids-mem/` project dir via `.env` or shell profile.

---

## 4. ID Format

**Decision: ULID with prefix**

| Entity | Prefix | Example |
|---|---|---|
| Memory | `mem_` | `mem_01J9KXVR2E...` |
| Session | `sess_` | `sess_01J9KXVR2E...` |

Generate with: `github.com/oklog/ulid/v2`

ULIDs are:
- sortable by creation time
- URL-safe
- human-readable with prefix
- 128-bit, collision-resistant

---

## 5. Session ID Ownership

**Decision: binary generates if caller omits**

- Caller may pass `--session-id sess_01J...` to group saves in a run
- If omitted, binary generates a fresh ULID session ID and returns it in response
- Agent should capture returned `session_id` and reuse it for subsequent saves in the same run

No persistent "open session" state. Sessions are just a grouping label on memories. No `session_start` / `session_end` commands needed in V1.

---

## 6. Near-Duplicate Detection (Layer 2)

**Decision: BM25 top-K candidate retrieval + Jaccard similarity re-rank**

The original V0 used a single BM25 threshold (`rank < -15.0`). BM25 scores
are corpus-relative — the threshold drifts as the store grows and OR-joined
query terms inflate scores against long titles. Replaced with a two-stage
filter:

1. **Candidate retrieval**: top `bm25CandidateLimit = 20` rows by
   `bm25(memories_fts, 3, 1, 2, 1)` (title x3, learned x2, what x1, tags x1),
   filtered by same `task_type` and `kind`.
2. **Re-rank**: compute Jaccard similarity of normalized token sets
   (title+what+learned+tags) between request and each candidate. Token set
   excludes 1-2 char noise.
3. **Threshold**: similarity `>= jaccardDupeThreshold = 0.85` → treat as
   near-duplicate, return `status: "skipped", reason: "near_duplicate"`.

Jaccard is corpus-size independent and length-bias free, so the constant
stays meaningful as the store grows.

**How to tune `jaccardDupeThreshold`**: integration test pairs of
known-near-dupe and known-distinct memories, print scores, adjust constant.
Target: near-dupes score >= 0.85, distinct memories score below.

**Layer 1** (exact Fingerprint match) is unchanged and runs first — Layer 2
only fires when Fingerprint misses.

---

## 7. Response Shapes

All commands return JSON to stdout. See PRD §8.1 for full shapes.

General envelope:

```json
{ "status": "saved|skipped|updated|error", ... }
```

Error shape (CLI-GUIDE §9):
```json
{
  "status": "error",
  "code": "validation_failed",
  "field": "kind",
  "message": "kind must be one of: error_resolution, task_pattern, user_rule, session_summary",
  "input": { "kind": "bad_value" },
  "retryable": false,
  "suggestion": "Set --kind to one of the allowed values"
}
```

- `code`: machine-readable error type
- `input`: echo of the failing field(s) so agent sees what it sent wrong
- `retryable`: `true` for transient errors (DB locked), `false` for bad input / validation
- `suggestion`: concrete fix the agent can act on

**Exit codes (CLI-GUIDE §6)**

| Code | Meaning |
|---|---|
| `0` | Success (saved, skipped, updated) |
| `1` | Runtime / unexpected error |
| `2` | Invalid arguments or usage error |
| `3` | Resource not found (e.g. `get --id` not found) |
| `5` | Conflict / duplicate (memory skipped) |
| `10` | Dry-run passed — no write occurred |

---

## 8. `context` FTS Query Source

**Decision: caller passes optional `--query` flag**

```
droids-mem context --task-type crm_upload --query "hubspot phone field" --limit 8
```

- If `--query` provided: use for FTS ranking of `error_resolution` and `task_pattern` slots
- If `--query` omitted: tokenize `task_type` value as fallback query
- `user_rule` and `session_summary` slots: recency-ranked only, no FTS

---

## Go Module Bootstrap

```
module github.com/yourusername/droids-mem

go 1.22
```

Key dependencies:
```
modernc.org/sqlite          # SQLite + FTS5, pure Go
github.com/oklog/ulid/v2    # ULID generation
github.com/spf13/cobra      # CLI subcommands, --help, flag parsing
```

**Flag conventions (CLI-GUIDE §2)**
- All flags long-form: `--task-type`, `--session-id`, `--dry-run`, `--no-interactive`
- No short-form aliases in V1
- Boolean negation via `--no-` prefix: `--no-interactive`
- `--format json` accepted on every command (JSON is also the non-TTY default)
- `--help` / `-h` responds on every subcommand; cobra handles this automatically

---

## Start Order

```
M0 ✅  decisions locked (this doc)
M1     db init: schema + 3 FTS triggers + AFTER INSERT updated_at trigger
M2     save: validation, normalize, fingerprint, insert
M3     dedupe: fingerprint check + BM25 pre-save check + force flag
M4     search: FTS query, exact filters, BM25 rank
M5     context: priority slots, cross-slot dedup
M6     CLI entry point: cobra subcommands, flag parsing, JSON output
M7     end-to-end: two-run test, second run uses first run memories
```
