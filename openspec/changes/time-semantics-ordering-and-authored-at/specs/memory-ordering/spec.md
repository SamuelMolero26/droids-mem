# Memory Ordering Specification

## Purpose

Deterministic, newest-first retrieval for every "top-N by recency" query
against `memories`, plus the indexes and session-summary retention behavior
that keep it correct under same-second ties and bulk imports.

## Requirements

### Requirement: Newest-first tiebreak on all recency-ordered queries

The system MUST order every recency-ranked read of `memories` by
`created_at DESC, id DESC` (never `created_at DESC` alone) at these sites:
`fetchLastSessionConn`, `fetchUserRulesConn` (context.go); the DELETE's
`NOT IN` subquery and the outer ordering of `pruneSessionSummariesConn`, and
the inner subquery plus outer `ORDER BY` of `pruneAutoSummariesConn`
(save.go); `List` and `RecentSessions` (inspect.go).

#### Scenario: Same-second user rules resolve newest-first

- GIVEN six `user_rule` rows for one `task_type`, all inserted within the
  same wall-clock second
- WHEN the context bundle's always-tier user-rules query runs
- THEN the 5 rows with the lexicographically greatest `id` (i.e. the most
  recently minted ULIDs) appear in the always tier
- AND the sixth (oldest-ID) row is demoted to a browse stub, not any other row

#### Scenario: Recency list is stable across repeated reads

- GIVEN a set of rows sharing one `created_at` second
- WHEN `List` or `RecentSessions` is called twice in a row with no writes
  in between
- THEN both calls return rows in the identical order

### Requirement: Session-summary retention keeps the newest rows

Retention (`pruneSessionSummariesConn`, `pruneAutoSummariesConn`) MUST
delete the OLDEST rows beyond the retained count and MUST NEVER delete a row
that is newer (by `created_at DESC, id DESC` order) than a row it keeps.

#### Scenario: Tie group of same-second summaries evicts the oldest, not the newest

- GIVEN 7 `session_summary` rows for one `task_type`, all saved within the
  same wall-clock second, and a retained count of 5
- WHEN retention runs
- THEN the 5 rows with the greatest `id` are kept
- AND the 2 rows with the smallest `id` are deleted
- AND no row with a greater `id` than a deleted row is ever deleted

### Requirement: Composite indexes support newest-first ordering without a temp B-tree

`idx_memories_task_kind_created`, `idx_memories_created_at`, and
`idx_memories_origin_created` MUST each be defined as 4-column indexes ending
in `created_at DESC, id DESC`, so that `ORDER BY created_at DESC, id DESC`
queries can satisfy their `ORDER BY` directly from the index.

#### Scenario: Query plan avoids a temp B-tree sort

- GIVEN the 4-column indexes are in place
- WHEN `EXPLAIN QUERY PLAN` runs against any of the six ordered queries
- THEN the plan does not contain `USE TEMP B-TREE FOR LAST TERM OF ORDER BY`

#### Scenario: Large same-second tie group after bulk import stays cheap

- GIVEN a bulk import stamps 2000+ rows with the same `created_at` second
- WHEN a `LIMIT`-bounded newest-first query runs against that tie group
- THEN the query plan uses the 4-column index's `ORDER BY`-satisfying scan,
  not a full tie-group sort

### Requirement: Index-changing migration rungs must DROP before CREATE

A migration rung that changes an existing index's column definition MUST
`DROP INDEX` before `CREATE INDEX`, at both `internal/db/migrations.go` (the
rung) and `internal/db/schema.go` (the fresh-DB DDL), in lockstep.

#### Scenario: CREATE INDEX IF NOT EXISTS alone is rejected as insufficient

- GIVEN an index already exists at an older column definition
- WHEN the rung ships only `CREATE INDEX IF NOT EXISTS` at the new
  definition
- THEN the requirement is not met — this pattern silently leaves upgraded
  databases on the old index shape while fresh databases get the new one

#### Scenario: Migrated and fresh databases agree on schema version and index shape

- GIVEN one database created fresh at the current `user_version` and one
  database migrated step-by-step from `user_version = 6` to the current
  `user_version`
- WHEN a drift guard compares index definitions between the two databases
  via `pragma_index_xinfo` (not `sqlite_master.sql`, whose text differs
  between DDL paths even when semantically identical)
- THEN every index definition (columns, order, direction) is identical
  between the two databases

#### Scenario: Comparing index names alone does not satisfy the drift guard

- GIVEN a rung that drops an index and recreates it under the same name but
  a different column definition
- WHEN a guard compares only index NAMES between migrated and fresh
  databases
- THEN that comparison is insufficient to catch the drift — the guard MUST
  compare column-level definitions, not names

### Requirement: ULID ordering, not row scan order, is the source of intra-second determinism

`id DESC` as a tiebreak MUST rely on `oklog/ulid/v2`'s monotonic entropy
source (minted at a single call site inside one `BEGIN IMMEDIATE`
transaction), not on SQLite's physical row/scan order or `rowid`.

#### Scenario: IDs are genuinely newest-first within one millisecond

- GIVEN multiple rows minted within the same millisecond by the single ID
  minting call site
- WHEN their `id` values are compared lexicographically
- THEN the lexicographic order matches the true minting order

#### Scenario: Cross-process same-millisecond writes still total-order deterministically

- GIVEN two different process instances mint an ID in the same millisecond
  (monotonic entropy is per-process, not global)
- WHEN their rows are ordered by `id DESC`
- THEN the result is a deterministic total order (some consistent
  ordering), even though it is not guaranteed to reflect true wall-clock
  write order across processes
