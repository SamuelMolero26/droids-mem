# Memory Provenance Specification

## Purpose

Carry a memory's original authoring date (`authored_at`) across the
shared-pool import/export boundary, distinct from `created_at` (when the
local row was inserted), and surface it on read paths — without wiring it
into any review/decay mechanism.

## Requirements

### Requirement: `authored_at` column exists on both memory tables

`memories` and `archived_memories` MUST have a `NOT NULL` `authored_at`
column, added by migration rung v7→v8, backfilled to each existing row's
`created_at`. `CurrentSchemaVersion` MUST be 8 after this rung.

#### Scenario: Legacy rows backfill to created_at

- GIVEN a database migrated from `user_version = 7`
- WHEN the v7→v8 rung completes
- THEN every pre-existing row's `authored_at` equals that row's `created_at`

#### Scenario: Fresh database starts at version 8

- GIVEN a brand-new database initialized from `schema.go`
- WHEN it is opened
- THEN `user_version` is 8 and `authored_at` is present and `NOT NULL` on
  both tables

### Requirement: A locally-authored save stamps authored_at equal to created_at

When `Save` is called without an explicit `AuthoredAt` (or with one that
fails the clamp), the system MUST set `authored_at` equal to the same `now`
used for `created_at`.

#### Scenario: Plain local save

- GIVEN a `Save` call with no `AuthoredAt` field set
- WHEN the row is inserted
- THEN `authored_at` equals `created_at` for that row

### Requirement: authored_at is clamp-only, never rejected

`resolveAuthoredAt` MUST clamp any `AuthoredAt <= 0` or `AuthoredAt` in the
future (relative to `now`) to `now`. It MUST NOT reject the save or count it
as a failure.

#### Scenario: Non-positive or future stamp is clamped, not rejected

- GIVEN an import or save request with `AuthoredAt` that is `<= 0` or
  greater than the current time
- WHEN the row is saved
- THEN `authored_at` is set to `now`
- AND the row is imported/saved successfully
- AND it is NOT counted in `ImportResult.Failed`

### Requirement: Import preserves the peer's authored_at; export carries it forward unchanged

`share.go` import MUST set the imported row's `authored_at` from the peer's
exported value (subject to the clamp above), while `created_at` is set to
the local import time. Export MUST serialize the row's own `authored_at`
unchanged.

#### Scenario: Imported row keeps peer authorship

- GIVEN a peer's exported memory with `authored_at` from three years ago and
  a valid clamp range
- WHEN it is imported locally
- THEN the local row's `authored_at` equals the peer's original value
- AND the local row's `created_at` equals the local import time (not the
  peer's original date)

#### Scenario: Re-export is byte-identical for authorship

- GIVEN a row was imported with a preserved `authored_at`
- WHEN that row is re-exported to a third peer
- THEN the re-exported `authored_at` equals the original author's stamp,
  unchanged by the intermediate import/export hop

### Requirement: authored_at is projected on single-record and list read paths only

`GetRow` (backing `mem_get`, the CLI, and the TUI detail pane) and `List`
MUST project `Memory.AuthoredAt`. `authored_at` MUST NOT appear in
`mem_search` or `mem_context` response payloads.

#### Scenario: TUI detail pane shows peer authoring date

- GIVEN an imported memory whose `authored_at` predates its `created_at`
- WHEN its TUI detail pane is rendered
- THEN the peer's authoring date is shown on its own detail line, distinct
  from the import (`created_at`) date

#### Scenario: mem_search and mem_context omit authored_at

- GIVEN any memory with `authored_at != created_at`
- WHEN it appears in a `mem_search` result or a `mem_context` bundle item
- THEN the response payload for that item does not include `authored_at`

### Requirement: Shared-scope retention fence protects personal session summaries

`pruneSessionSummariesConn` MUST add `AND scope = 'personal'` so that
imported (`scope = 'shared'`) session summaries are never counted toward, or
evicted by, the personal newest-5 retention window.

#### Scenario: Bulk-imported shared summaries do not evict personal ones

- GIVEN a user has 5 personal `session_summary` rows for one `task_type`
- WHEN 10 shared `session_summary` rows are bulk-imported for the same
  `task_type`, all newer than the personal rows
- THEN all 5 personal rows remain after retention runs
- AND the 10 shared rows are not deleted by the personal retention pass
  (they are unbounded by this fence, per accepted risk)

#### Scenario: A personal summary explicitly shared still leaves personal retention

- GIVEN a personal `session_summary` row that is explicitly re-scoped to
  `shared`
- WHEN personal retention next runs
- THEN that row is no longer counted among the 5 personal rows retention
  protects (accepted consequence of the fence)

### Requirement: review_after and needs_review stay inert

The system MUST NOT write to `review_after` on any code path (insert,
`ON CONFLICT DO UPDATE`, `forceUpdateConn`, or import). `review_after` MUST
remain `NULL` on every row, and `needs_review` MUST remain `false` on every
row, exactly as at HEAD before this change.

#### Scenario: review_after stays NULL after every write path

- GIVEN a fresh save, a force-updated save, and an imported row
- WHEN each of the three write paths completes
- THEN `review_after` is `NULL` on the resulting row in all three cases

#### Scenario: needs_review is never true

- GIVEN any row in the database, including one whose `authored_at` is years
  in the past
- WHEN `needs_review` is evaluated for that row
- THEN it is `false`

(Note: `decayHorizonDays`, `reviewAfterFor()`, and every `review_after`
write introduced by the Option-1 implementation are removed by this
requirement — they are dead code under the provenance-only design and MUST
NOT be reintroduced dormant.)
