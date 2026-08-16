# Design: Time Semantics — Newest-First Ordering and `authored_at` Provenance

## Technical Approach

Two stacked slices over the existing layered store. **PR A** (`develop`) makes
newest-first retrieval deterministic: `id DESC` tiebreak at six query sites,
three 4-column index redefinitions, migration rung **v6→v7**, and a
definition-level drift guard. **PR B** (branches from PR A) adds `authored_at`
as pure provenance: rung **v7→v8**, `CurrentSchemaVersion = 8`, carry through
`Save`/`share.go`, projection in `GetRow` + `List`, one TUI detail line, and the
`scope = 'personal'` retention fence.

The staged tree collapses both slices into a single rung `migrationV6ToV7` at
`CurrentSchemaVersion = 7` and implements the rejected decay option. Apply must
split it, not extend it.

## Architecture Decisions

| # | Decision | Choice | Rejected | Rationale |
|---|---|---|---|---|
| D1 | Tiebreak key | `id DESC` | `rowid DESC`; a ms column | ULID (`oklog/ulid/v2`, `LockedMonotonicReader`) is column data, newest-first within a ms, minted once inside `BEGIN IMMEDIATE`. `rowid` is storage location: a table-rebuild reassigns it and ordering inverts silently — no error, no failing test. |
| D2 | Ladder shape | Two rungs: v6→v7 indexes, v7→v8 `authored_at` | One combined rung (staged) | Each PR must own one rung. PR A ships to `develop` first; DBs that reach v7 in the field must be upgradable by PR B. A combined rung would force PR B to rewrite PR A's history. |
| D3 | Index rung form | `DROP INDEX IF EXISTS` then bare `CREATE INDEX` | `CREATE INDEX IF NOT EXISTS` only | `IF NOT EXISTS` is a **no-op** against an index that already exists under the old definition: upgraded DBs stay 3-column, fresh DBs get 4-column, same `user_version`, nothing errors. Bare `CREATE` after the `DROP` also fails loud if the drop did not happen. |
| D4 | Drift guard | Compare index **definitions** via `pragma_index_xinfo` | `indexNames()`; `sqlite_master.sql` text | A `DROP`+`CREATE` reusing the name walks past a name check. `sqlite_master.sql` differs by whitespace and `IF NOT EXISTS` between the DDL and migration paths, so text comparison is a false-failure generator. |
| D5 | Backfill | `ADD COLUMN ... NOT NULL DEFAULT 0` then `UPDATE ... SET authored_at = created_at` | Leave legacy rows at 0 | SQLite has no `ADD COLUMN ... DEFAULT (<other column>)`, so two statements per table. Epoch-0 rows are wrong even with no decay consumer: `GetRow`/`List` project the column, and the TUI would render every legacy memory as authored 1970. `created_at` is the closest honest value. |
| D6 | `ON CONFLICT` | `authored_at = excluded.authored_at`; `origin` still excluded | Preserve existing `authored_at`; `MIN(...)` | Path is defensive-only (layer-1 fingerprint dedupe returns `skipped` inside the same `BEGIN IMMEDIATE`). It is consistent, not contradictory, with `origin`: `origin` records the row's *creation channel*, `authored_at` describes the *current body* — and the conflict clause overwrites the body. Same rule `forceUpdateConn` already applies. |
| D7 | Trust boundary | Clamp-only in `Save.resolveAuthoredAt` (`<= 0` or `> now` → `now`) | Reject into `ImportResult.Failed` (ADR-0033 §6 prose) | Documented behaviour is false against the code; fix the prose. A hostile/skewed stamp can only be flattened to "authored now" — never used to accelerate anything, since nothing reads it. Losing a whole lesson to peer clock skew is the worse failure. Layering is correct: `Save` is the single write choke point (CLI, MCP, import); clamping in `importLine` would leave the other writers unguarded. `mem_save`'s `saveArgs` does not expose the field, so import is its only real writer today. |
| D8 | Read surface | `Memory.AuthoredAt` in `GetRow` + `List` only | Also `search.go` / `context.go` | `GetRow` feeds `mem_get`, CLI `get`, and the TUI detail pane — one projection lights all three. Spraying the field across bundles repeats the `needs_review` mistake: an unexplained field with no consumer, paid for on every call. |
| D9 | Retention fence | `AND scope = 'personal'` in PR B | PR A | It fixes an *import* bug (`importLine` leaves `origin='manual'`, `created_at=now`), not an ordering bug, and it relieves PR A's line budget. |
| D10 | Decay code | Delete `decayHorizonDays` + `reviewAfterFor` | Leave dormant | "Only add functionality that is small or absolutely necessary." Option 2 is a strict subset of Option 1 and re-layers later with **no additional migration**. |

## Migration Ladder Mechanics

```
v6 ──PR A──▶ v7                       ──PR B──▶ v8
      DROP+CREATE ×3 (4-col indexes)        ALTER memories          ADD authored_at NOT NULL DEFAULT 0
                                            UPDATE memories         SET authored_at = created_at
                                            ALTER archived_memories ADD authored_at NOT NULL DEFAULT 0
                                            UPDATE archived_memories SET authored_at = created_at
```

- `applyMigration` wraps each rung in a transaction including the
  `PRAGMA user_version` bump, so a failed `CREATE` rolls back to v6/v7 intact.
- **`schema.go` lockstep**: PR A moves the three `CREATE INDEX` lines to the
  4-column shape only; PR B adds `authored_at` to `memories` *and*
  `archived_memories` in `ddlTables`. Shipping either half early breaks
  `TestInit_FreshMatchesMigratedShape` (columns and index defs) — the guard is
  self-enforcing, which is why the column must not ride in PR A's DDL.
- **`archived_memories` needs nothing beyond column + backfill.** It has no FTS,
  no triggers, and no `INSERT` anywhere in the repo. The `UPDATE` is a no-op on
  an empty table; it is kept for symmetry and to stay correct if an archive
  writer lands later. The column itself is mandatory: the existing
  `PRAGMA table_info` parity test fails without it.
- SQL comments inside `schema.go`'s backtick raw strings must use double quotes
  — a backtick terminates the literal.

## Drift Guard Shape

The staged tree already adds `indexDefs()` (`pragma_index_xinfo`, `key = 1`,
`name || ' DESC'`) and swaps `TestInit_FreshMatchesMigratedShape`'s index
assertion from `indexNames` to `indexDefs`. That is the right shape; keep it.
`indexNames()` stays for the two existence-only assertions
(`TestInit_FreshDBHasOriginColumnAndIndex`, `TestMigrate_V2toV3...`) — presence
checks are legitimate there. **What changes**: the fresh-vs-migrated equivalence
assertion, and only that one, moves from names to definitions.

## Data Flow

    pool JSONL ──▶ ImportShared ──▶ importLine ──▶ Save
                                                    │ resolveAuthoredAt(clamp)
                                                    ▼
                                         memories.authored_at (never an ORDER BY key)
                                                    │
                        GetRow / List ──────────────┘
                          │      │
                mem_get / CLI    TUI detail pane

## Walk-Back From the Staged Diff (verified against the tree)

| Location (confirmed) | Action |
|---|---|
| `save.go:124-128` `decayHorizonDays` | Delete |
| `save.go:130-141` `reviewAfterFor()` | Delete |
| `save.go:322` `reviewAfter := reviewAfterFor(...)` | Delete |
| `save.go:328/329/341` INSERT column list, placeholder, `ON CONFLICT ... review_after = excluded.review_after` | Drop `review_after`; keep `authored_at` |
| `save.go:343` bind args | Drop the `reviewAfter` argument |
| `save.go:562-573` `forceUpdateConn` comment, `review_after=?`, `reviewAfterFor(...)` arg | Drop the write; keep `authored_at=?` |
| `save.go:113-118` `SaveRequest.AuthoredAt` doc, `save.go:143-147` `resolveAuthoredAt` doc | Rewrite — both cite the decay horizon |
| `schema.go:35-40` `authored_at` comment | Rewrite — drop "feeds the decay horizon" |
| `migrations.go:225-256` combined rung | Split into v6→v7 (indexes) and v7→v8 (column+backfill); bump `CurrentSchemaVersion` to 8 in PR B |
| `share.go:140-141`, `:26-31` comments | Rewrite — provenance, not decay |
| `authored_at_test.go` | Rewrite (below) |

Invariant after PR B: `review_after` is NULL on every path, `needs_review`
permanently false.

## File Changes

| File | PR | Action |
|---|---|---|
| `internal/db/migrations.go` | A / B | v6→v7 index rung / v7→v8 column rung + version bump |
| `internal/db/schema.go` | A / B | 4-col index DDL / `authored_at` on both tables |
| `internal/db/migrations_test.go` | A / B | `indexDefs` guard + rung tests |
| `internal/db/eqp_test.go` | A | See testing strategy — currently a false-negative |
| `internal/store/context.go` | A | `id DESC` ×2 |
| `internal/store/inspect.go` | A / B | `id DESC` ×2 / `AuthoredAt` field + `List` + `GetRow` projections |
| `internal/store/save.go` | A / B | `id DESC` ×4 / `authored_at` carry, scope fence, decay removal |
| `internal/store/share.go` | B | Export/import `authored_at` (already staged, comments rewritten) |
| `internal/store/retention_test.go` | A / B | Tie-group eviction / scope fence |
| `internal/store/authored_at_test.go` | B | New, provenance-only |
| `internal/tui/view.go` | B | One authorship line in `renderDetail` |
| `CLAUDE.md` | A | Invariant: index-changing rungs must `DROP` first |

## Interfaces

```go
// store.Memory (inspect.go) — projected by GetRow and List only.
AuthoredAt int64 `json:"authored_at"`
```

## Testing Strategy

| Layer | What | Approach |
|---|---|---|
| Unit (store) | Same-second tie keeps newest | Seed rows directly with a **fixed** `created_at` and explicit ids of known lexical order; one real `Save` fires the prune |
| Unit (store) | Scope fence | Same fixture, two `scope='shared'` rows with ids sorting *above* every personal id |
| Unit (store) | `authored_at` carry + clamp | `ImportShared` over a JSONL fixture; assert `authored_at` preserved, `created_at >= now`, `review_after` NULL |
| Unit (db) | Rung correctness | `newPreV1DB` → `Migrate` → assert `user_version`, column present, backfill == `created_at` |
| Unit (db) | Drift | `indexDefs(fresh)` vs `indexDefs(migrated)` |
| Unit (db) | Planner | `EXPLAIN QUERY PLAN` on the tiebroken queries |
| Unit (tui) | Detail line | Direct `renderDetail` call, assert the authored date appears when it differs from `created_at` |

**Deterministic ties.** Wall-clock fixtures are racy. The staged
`retention_test.go` approach is **sound** and should be kept: `seedSummary`
inserts directly with `created_at = 1700000000` for every row and ids
`fmt.Sprintf("mem_%026d", i)`, so tie membership and lexical order are both
fixed; only the final triggering `Save` uses real time and is unambiguously
newest. Direct INSERT also bypasses both dedupe layers, so seeded rows cannot
be silently swallowed.

**Fixture trap.** Multi-save fixtures with near-identical bodies trip the
Jaccard near-duplicate gate (≥ 0.85) and return `skipped` with an empty `ID`.
Any test that goes through `Save` more than once needs dissimilar bodies and
must assert `resp.Status == "saved"` before using `resp.ID` — the staged
retention test already does both.

**`eqp_test.go` is currently a false negative and is PR A's RED test.** Its
three queries `ORDER BY created_at DESC` with no `, id DESC`, and it asserts on
`"USE TEMP B-TREE FOR ORDER BY"` — the string SQLite actually emits for a
partially-covered ordering is `"USE TEMP B-TREE FOR LAST TERM OF ORDER BY"`,
which that `strings.Contains` never matches. PR A must (1) add `, id DESC` to
each query so it mirrors production, (2) assert on the substring
`"USE TEMP B-TREE"` so both forms fail, and (3) add a
`prune_auto_summaries`/`recent_sessions` case for
`idx_memories_origin_created` and a `list` case for `idx_memories_created_at`.
Under strict TDD this test fails before the index change and passes after.

**`authored_at_test.go` rewrite.** Keep `TestSave_AuthoredAtDefaultsToCreatedAt`
(drop its `review_after` assertions), keep the import-preserves and
future-clamp tests (drop the horizon assertions), replace
`TestSave_SessionSummaryExemptFromDecay` with
`TestSave_NeverWritesReviewAfter` covering plain save, force-save, and import.

## Threat Matrix

N/A — no routing, shell, subprocess, VCS/PR automation, executable-file
classification, or process-integration boundary. The pool import is a *data*
trust boundary, handled by D7.

## Migration / Rollout

Auto-applied at boot via the ladder; no operator action, no `--rescrub` (FTS is
untouched — `memories_fts` has no `authored_at`, no trigger changes). Rollback:
revert the commit; the extra rung and column are inert. PR A's 4-column index is
a strict superset of the 3-column one, so reverted code still plans well.

## Sequencing and Conflict Risk

PR B branches from PR A's branch, so PR B's diff shows only its own work.
Overlaps and how they resolve:

- `migrations.go`: PR A ends the file with `migrationV6ToV7`; PR B **appends**
  `migrationV6ToV7`'s successor and touches two other lines (`CurrentSchemaVersion`,
  the `migrations` slice). Append-only — no textual conflict.
- `save.go` `pruneSessionSummariesConn`: PR A adds `ORDER BY created_at DESC, id DESC`;
  PR B adds `AND scope = 'personal'` to the outer `WHERE` and the subquery.
  Adjacent lines in one statement — PR A must ship this function **without**
  the fence and without the fence sentence in its doc comment, and the staged
  tree already has both. This is the one hunk the apply phase must hand-split.
- `schema.go`: PR A touches only the three `CREATE INDEX` lines; PR B only the
  two table bodies. Disjoint.

## Open Questions

- [x] **Seventh ordering site — RESOLVED: folded into PR A.**
      `internal/store/store.go:40` (`ListTaskTypes`) has
      `... kind='session_summary' ORDER BY created_at DESC LIMIT 1` for the TUI
      census's "latest session" title — same tie bug, display-only, one line,
      already covered by `idx_memories_task_kind_created`. PR A therefore fixes
      SEVEN sites, not six. Shipping a change titled "make newest-first
      deterministic" while knowingly leaving an instance of the same tie unfixed
      is worse than one extra line: the next reader cannot tell whether it was
      missed or deliberate.
- [x] Detail-only provenance (proposal Q1) — RESOLVED: detail pane only. The TUI
      *list* row keeps showing `created_at`, which is what "when did this enter
      my store" means; the detail pane is one keystroke away.
- [x] Shared-summary retention cap (proposal Q2) — RESOLVED: later. Bounded by
      fingerprint dedupe and surfaced by doctor growth warnings. A second prune
      function for a case nothing has measured is not small or necessary.
- [x] `serverInstructions` sentence (proposal Q4) — RESOLVED: deferred on
      measurement. `authored_at` DOES reach `mem_get`, because `GetRow` is the
      shared choke point behind `mem_get`, the CLI, and the TUI detail pane. The
      instructions sentence is what is deferred: the live corpus is 105 rows
      with zero provably imported, so `authored_at` equals `created_at`
      everywhere and the sentence would bill tokens on every MCP initialize to
      teach a distinction with no instances. Revisit when a pool carries
      imported rows.
