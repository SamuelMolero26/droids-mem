# Proposal: Time Semantics — Newest-First Ordering and `authored_at` Provenance

## Intent

`created_at` has 1-second resolution and is the sole sort key of six newest-N
queries. Same-second rows tie; SQLite walks a tie group oldest-first, so
`LIMIT 5` keeps the OLDEST five. Two surfaces are hit: `context.go` demotes a
fresh `user_rule` to a browse stub (recoverable), and `pruneSessionSummariesConn`
feeds a DELETE — a NEWER session summary is destroyed, an older one kept
(unrecoverable). Bulk `ImportShared` re-stamps every row to one `now`,
manufacturing exactly the tie group that triggers it.

Separately, imported pool memories lose their origin date: `created_at` becomes
the import date, so a three-year-old peer lesson is indistinguishable from one
written today. The user cannot judge what they are reading.

Ship as **two stacked PRs**: PR B branches from PR A.

## Scope

### In Scope — PR A (`fix/58` — ordering correctness)

- `id DESC` as secondary sort at six sites: `context.go` (`fetchLastSessionConn`,
  `fetchUserRulesConn`), `save.go` (`pruneSessionSummariesConn` — both the
  DELETE's `NOT IN` subquery and its own ordering; `pruneAutoSummariesConn` —
  inner subquery and outer ORDER BY), `inspect.go` (`List`, `RecentSessions`).
- Three index redefinitions to 4 columns: `idx_memories_task_kind_created`,
  `idx_memories_created_at`, `idx_memories_origin_created`.
- Migration rung **v6→v7**: `DROP INDEX` then `CREATE`, in lockstep with
  `schema.go` DDL.
- Drift guard test comparing index *definitions* via `pragma_index_xinfo`.

### In Scope — PR B (`authored_at` — provenance only)

- Migration rung **v7→v8**: `authored_at` on `memories` and `archived_memories`,
  backfilled to `created_at`. `CurrentSchemaVersion` = 8.
- Carry: `Save` accepts it, `resolveAuthoredAt` clamps, `share.go` exports and
  imports it.
- Read surface: `Memory.AuthoredAt` projected in `GetRow` + `List`, one TUI
  detail line. `GetRow` is the choke point behind `mem_get`, the CLI, and the
  TUI detail pane, so one projection lights up all three.
- `AND scope = 'personal'` fence on `pruneSessionSummariesConn`.

### Out of Scope

- `authored_at` driving `review_after`. `needs_review` stays permanently false.
- Adding `authored_at` to `mem_search` / `mem_context` bundles.
- A `serverInstructions` sentence teaching the agent to weigh authoring age.
  Deferred on measurement: the live corpus holds 105 rows, of which 3 are
  `scope='shared'` and none are provably imported, and both remembered pool
  paths point at directories that do not exist. `authored_at` would equal
  `created_at` on every row today, so the sentence would spend tokens on every
  MCP initialize to teach a distinction that has no instances. Revisit when a
  pool actually carries imported rows.
- Implementing `mark-reviewed`, archive-on-supersede, or a shared-summary cap.
- `rowid` as a tiebreak — storage location, not data; a table rebuild silently
  inverts ordering.

## Capabilities

### New Capabilities

- `memory-ordering`: deterministic newest-first retrieval, its supporting
  indexes, and session-summary retention.
- `memory-provenance`: origin date carried across the shared-pool boundary.

### Modified Capabilities

- None — no `openspec/specs/` exists yet.

## Approach

**Tiebreak on `id DESC`.** ULIDs from the pinned `oklog/ulid/v2 v2.1.1` use
`LockedMonotonicReader`, so IDs are genuinely newest-first within a
millisecond, minted at a single site inside `BEGIN IMMEDIATE` with a constant
`mem_` prefix. Cross-process same-millisecond commits degrade to a deterministic
total order, never to scan order. (ADR-0032's claim that `ulid.Make()` draws
fresh entropy is factually wrong and must be corrected.)

**4-column indexes are mandatory, not tuning.** With the 3-column index,
`EXPLAIN QUERY PLAN` shows `USE TEMP B-TREE FOR LAST TERM OF ORDER BY`, which
kills LIMIT's early exit. Cost scales with tie-group size: one 2000-row tie
group costs 720µs vs 13µs on the 4-column index — 55x. `ImportShared`
manufactures that group.

**`CREATE INDEX IF NOT EXISTS` is a no-op against an existing index of a
different definition.** An IF-NOT-EXISTS-only rung leaves upgraded DBs on the
old shape while fresh DBs get the new one — same `user_version`, different
schema, nothing errors. Hence `DROP` first, plus a definition-level drift guard.
Comparing index *names* walks straight past a `DROP`+`CREATE` that reuses the name.

**Retention fence belongs in PR B.** `importLine` sets `Scope: "shared"` but
omits origin, so imported rows land `origin='manual'` with `created_at = now` —
dead centre in the manual newest-5 prune, beating the user's own summaries.
This is an import bug, independent of issue #58: `Save` stamps
`created_at = time.Now().Unix()`, so every imported summary is strictly newer
than every pre-existing local one and beats it on `created_at` alone. The
tiebreak only orders rows *within* one second — for a bulk import, that means
among the imported rows themselves. So PR A neither causes nor worsens this,
and the fence ships with the import work in PR B, which also relieves PR A's
budget. Accepted consequence: a locally-authored summary that is
explicitly shared also leaves retention, and shared summaries grow uncapped —
bounded by fingerprint dedupe, surfaced by doctor growth warnings.

**Provenance only.** `review_after` has zero writers at HEAD, `needs_review` has
zero consumers (never rendered by the TUI, never named by `serverInstructions`,
no CLI verb reads it). Wiring `authored_at` into a flag nobody reads is not
"the most important case". Option 2 is a strict subset of Option 1 and can be
layered up later with **no additional migration**.

**Display is not deferred.** A write-only column is dead weight and the entire
justification for Option 2 is provenance the user can *see*. But `GetRow` is a
single choke point feeding the TUI detail pane, `mem_get` (via `Get`), and the
CLI — so the read surface costs ~15 lines. It stops there: spraying
`authored_at` across `mem_search`/`mem_context` would repeat the `needs_review`
mistake of shipping an unexplained field with no consumer.

**Trust boundary is clamp-only.** `resolveAuthoredAt` neutralises `<= 0` and
`> now` to `now` and rejects nothing. ADR-0033 §6's claim that `importLine`
rejects absurd values into `ImportResult.Failed` is false against the code. The
clamp is correct — a hostile or skewed future stamp can only be flattened to
"authored now" — and must be documented, not "fixed".

## PR B walk-backs from the staged diff

The staged tree implements Option 1. Remove:

| Location | Action |
|---|---|
| `save.go` `decayHorizonDays` (~121-128) | Delete — dead under Option 2 |
| `save.go` `reviewAfterFor()` (~130-141) | Delete |
| `save.go` INSERT + `ON CONFLICT DO UPDATE` (~322-345) | Drop `review_after` column and param; keep `authored_at` |
| `save.go` `forceUpdateConn` (~564-573) | Drop the `review_after` write |
| `internal/store/authored_at_test.go` | Rewrite — it asserts a plain local save gets a `review_after`; false under Option 2 |
| `save.go` doc comments on `AuthoredAt` / `resolveAuthoredAt` | Rewrite — they cite the decay horizon as the rationale |

Invariant after PR B: `review_after` is NULL on every path, exactly as today.

## Affected Areas

| Area | PR | Impact | Description |
|---|---|---|---|
| `internal/db/migrations.go` | A, B | Modified | v6→v7 index rung; v7→v8 `authored_at` rung |
| `internal/db/schema.go` | A, B | Modified | 4-col index DDL; `authored_at` on both tables |
| `internal/store/context.go` | A | Modified | `id DESC` × 2 |
| `internal/store/inspect.go` | A, B | Modified | `id DESC` × 2; `AuthoredAt` field + 2 projections |
| `internal/store/save.go` | A, B | Modified | `id DESC` × 4; `authored_at` carry, scope fence, decay removal |
| `internal/store/share.go` | B | Modified | Export/import `authored_at` |
| `internal/tui/view.go`, `model.go` | B | Modified | One authorship line in the detail pane |
| `internal/db/migrations_test.go` | A, B | Modified | Rung tests + `pragma_index_xinfo` drift guard |
| `internal/store/retention_test.go` | A, B | New | Tie-group eviction (A); scope fence (B) |
| `internal/store/authored_at_test.go` | B | New | Rewritten for provenance-only |
| `docs/adr/0033-*.md` | A, B | Rewritten | Option 2; gitignored, zero review lines |
| `CLAUDE.md` | A | Modified | Invariant: index-changing rungs must `DROP` first |

## Review budget forecast

Staged diff is 460 lines / 11 files, covering both slices.

| Slice | Forecast (add+del) | Budget risk |
|---|---|---|
| PR A | ~230–300 | Medium — the `pragma_index_xinfo` drift guard and `retention_test.go` are the bulk |
| PR B | ~230–250 | Low |

`400-line budget risk: Medium` — both slices fit, but PR A has the least
headroom. If the drift guard grows past ~120 lines, split it into a PR A2.

## Risks

| Risk | Likelihood | Mitigation |
|---|---|---|
| Index rung drops an index and the `CREATE` fails mid-migration | Low | Rung runs in a transaction; indexes are derivable — rollback restores v6 |
| `schema.go` DDL and migration rung diverge | Med | Definition-level drift guard test via `pragma_index_xinfo` (not `sqlite_master.sql`, whose whitespace differs between paths) |
| Backtick inside an SQL comment terminates the Go raw string in `schema.go` | Med | Use double quotes inside SQL comments in backtick literals — fails at build, caught by the gate |
| `authored_at` backfill on a large corpus | Low | Single `UPDATE` per table; no index on the column |
| Shared session summaries grow uncapped after the fence | Med | Accepted; fingerprint dedupe bounds it, doctor growth warnings surface it |
| PR B rebase conflict on `save.go` after PR A merges | Med | PR B is based on PR A's branch, not the trunk |

## Branching

PR A targets **`develop`** — the repo's integration branch (`#79`, `#81`, `#82`
all merged there; `main` receives release merges such as `#68`). PR B targets
PR A's branch and only reaches `develop` once PR A merges.

## Rollback Plan

- **PR A**: revert the commit. Indexes revert to the 3-column shape via a v7→v6
  down-path or simply by leaving v7 in place (the 4-column index is a strict
  superset — the old queries still use it). Ordering reverts to scan order.
- **PR B**: revert the commit. `authored_at` stays in the schema (harmless,
  backfilled to `created_at`) — no data is lost because nothing reads it.
- Neither slice deletes rows or changes `created_at`, so no data-loss rollback
  path is needed.

## Dependencies

- PR B must be based on PR A's branch (stacked, not parallel).
- No new Go dependencies. `oklog/ulid/v2 v2.1.1` stays pinned — its
  monotonic reader is load-bearing for the tiebreak.

## Success Criteria

- [ ] A `user_rule` saved in the same second as five others appears in the
      always-tier, not as a browse stub.
- [ ] A session summary saved in the same second as five others evicts the
      OLDEST, never a newer one.
- [ ] `EXPLAIN QUERY PLAN` on the six queries shows no
      `USE TEMP B-TREE FOR LAST TERM OF ORDER BY`.
- [ ] A DB upgraded v6→v7 and a freshly created DB have byte-identical index
      definitions, asserted by the drift guard.
- [ ] Imported shared summaries never evict a personal one.
- [ ] `CurrentSchemaVersion` = 8; `authored_at` on legacy rows = `created_at`.
- [ ] An imported memory's TUI detail pane shows the peer's authoring date, not
      the import date.
- [ ] `review_after` is NULL on every row after any save/force-save/import.
- [ ] `go build ./cmd/droids-mem`, `go test -count=1 ./...`, `go vet ./...`,
      `golangci-lint run --timeout 5m` all clean on both slices.

## Proposal question round

Interactive mode, but this executor cannot prompt directly. Four open product
questions — assumptions taken are stated; correct any before `sdd-spec`.

1. **Provenance display depth.** Assumed: `GetRow` + `List` projection and one
   TUI *detail* line. The TUI *list* row still shows only `created_at`, so a
   stale imported lesson looks fresh while browsing. Is detail-only enough, or
   should the list row show the authored date for shared rows?
2. **Uncapped shared summaries.** Assumed: acceptable, bounded by dedupe and
   surfaced by doctor. Is a separate shared-scope retention cap wanted now, or
   genuinely later?
3. **Dead decay code.** Assumed: `decayHorizonDays` and `reviewAfterFor` are
   *deleted*, not left dormant, per the startup practice. Confirm — keeping them
   would make Option 1 a one-line re-enable but leaves unreachable code.
4. **Agent-facing provenance.** Assumed: `authored_at` stays out of
   `mem_search`/`mem_context`. But the agent, not the human, is the primary
   reader of imported lessons. Should `mem_get` responses plus one sentence in
   `serverInstructions` teach the agent to weigh authoring age — closing the gap
   ADR-0031 §3 left open for `needs_review`?
