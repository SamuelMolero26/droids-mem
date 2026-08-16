# Tasks: Time Semantics — Newest-First Ordering and `authored_at` Provenance

## Review Workload Forecast

| Field | Value |
|---|---|
| Estimated changed lines | PR A ~290, PR B ~230-250 |
| 400-line budget risk | Low — both slices fit; PR A has the least headroom |
| Chained PRs recommended | Yes |
| Suggested split | PR A → PR B (2-way stack) |
| Delivery strategy | ask-always |
| Chain strategy | feature-branch-chain — PR A base=`develop`; PR B base=PR A branch |

Decision needed before apply: No — resolved, two slices.
Chained PRs recommended: Yes
Chain strategy: feature-branch-chain
400-line budget risk: Low

**Resolved: two PRs, not three.** This phase originally proposed splitting PR A
along the `internal/db` / `internal/store` package boundary into PR A + PR A2,
on a forecast of 300-420 combined lines. Recomputing from the staged diffstat
per slice — migrations 36 + migrations_test 39 + schema 30 + context 4 +
inspect 4 + inspect_test 10 + retention_test ~80 + save.go ~25 = ~228, plus ~60
for the `eqp_test.go` rework and 1 for the 7th ordering site — gives ~290, under
budget. The inflated upper bound was the entire rationale for the third PR.

The split was also wrong on its merits: neither half fixes anything alone.
PR A would widen the indexes to four columns while every query still ordered on
`created_at` alone, and PR A2 would add the call sites — so the retention
`DELETE` keeps discarding newer session summaries until both merge. The review
budget is a reviewer-burden heuristic, not a correctness constraint, and it does
not outrank time-to-fix on a bug that destroys user data. Standing rule: split
for genuinely independent units of meaning, never to hit a line count.

### Suggested Work Units

| Unit | Goal | PR | Focused test | Runtime harness | Rollback boundary |
|---|---|---|---|---|---|
| 1 | 4-col index redef, rung split, `eqp_test.go` RED fix, drift guard, `id DESC` at 7 sites, ordering retention RED test | PR A | `go test ./internal/db/... ./internal/store/...` | `mem_context`/`mem_save` MCP round-trip | Revert commit; 4-col index is a strict superset and ordering reverts to scan order |
| 2 | `authored_at` carry, `GetRow`/`List`, TUI line, scope fence, decay removal | PR B | `go test ./internal/store/... -run 'AuthoredAt\|Import\|Prune'` | `droids-mem share` export/import round-trip + TUI detail pane | Revert commit; column inert, `review_after` stays NULL |

## PR A — deterministic newest-first ordering (`internal/db` + `internal/store`), base = `develop`

- [x] 0.1 Pre-flight: check `PRAGMA user_version` + `pragma_table_info('memories')` on local dev DBs; any DB already at v7 with `authored_at` present (staged-build artifact) needs a documented reset before applying
- [x] 1.1 RED: rework `internal/db/eqp_test.go` — add `, id DESC` to all three probe queries to mirror production; assert on substring `"USE TEMP B-TREE"` (not the LAST-TERM-qualified full string); add cases for `idx_memories_origin_created` and `idx_memories_created_at`; run `go test ./internal/db/... -run TestEQP`, confirm it fails
- [x] 1.2 GREEN: split `migrationV6ToV7` in `internal/db/migrations.go` — strip the `authored_at` ALTER/UPDATE pairs (move to PR B); keep only the 3 `DROP INDEX IF EXISTS` + `CREATE INDEX` pairs (4-column, `created_at DESC, id DESC`)
- [x] 1.3 GREEN: `internal/db/schema.go` — 3 `CREATE INDEX` lines to 4-column shape; comments only, no `authored_at` column here (PR B owns it)
- [x] 1.4 Add `TestMigrate_V6toV7RedefinesIndexes` to `internal/db/migrations_test.go`: pre-v7 DB → `Migrate` → `indexDefs` shows the 4-column shape
- [x] 1.5 Confirm `TestInit_FreshMatchesMigratedShape`'s `indexDefs` guard passes fresh-vs-migrated
- [x] 1.6 RED: `internal/store/retention_test.go` — ordering-only variant (drop scope-fence assertions) of `TestSave_RetentionKeepsNewestUnderSameSecondTies`, using `seedSummary` direct INSERT (fixed `created_at=1700000000`, `mem_%026d` ids) plus one real trigger `Save`; confirm it fails against oldest-first eviction
- [x] 1.7 GREEN: `context.go` `fetchLastSessionConn`, `fetchUserRulesConn` — `ORDER BY created_at DESC, id DESC` (staged; verify)
- [x] 1.8 GREEN: `save.go` `pruneSessionSummariesConn` — `ORDER BY created_at DESC, id DESC` in the DELETE `NOT IN` subquery and the outer statement only; no `scope='personal'` fence, no fence sentence in the doc comment (hand-split from the staged combined hunk)
- [x] 1.9 GREEN: `save.go` `pruneAutoSummariesConn` — `id DESC` in the inner subquery and outer `ORDER BY`
- [x] 1.10 GREEN: `inspect.go` `List`, `RecentSessions` — `ORDER BY created_at DESC, id DESC`
- [x] 1.11 GREEN: `store.go:40` `ListTaskTypes` correlated subquery — add `, id DESC` (7th site, scope addition)
- [x] 1.12 Doc: append the DROP-before-CREATE index-migration invariant to `CLAUDE.md`
- [x] 1.13 Gates: `go build ./cmd/droids-mem`, `go vet ./...`, `go test -count=1 ./...`, `golangci-lint run --timeout 5m`

## PR B — `authored_at` provenance, base = PR A branch

- [x] 3.1 RED: rewrite `internal/store/authored_at_test.go` — drop `review_after`/decay assertions from `TestSave_AuthoredAtDefaultsToCreatedAt` and the two import tests; replace `TestSave_SessionSummaryExemptFromDecay` with `TestSave_NeverWritesReviewAfter` (plain save, force-save, import all assert `review_after IS NULL`)
- [x] 3.2 RED: add the scope-fence case to `retention_test.go` — 5 personal + 10 newer `scope='shared'` summaries via `seedSummary` direct INSERT (avoids the Jaccard near-dup gate; no repeated `Save` calls with similar bodies), assert personal rows survive
- [x] 3.3 GREEN: add `migrationV7ToV8` to `migrations.go` — `authored_at` ADD COLUMN + backfill on `memories` and `archived_memories`; bump `CurrentSchemaVersion` to 8; append to `migrations` slice
- [x] 3.4 GREEN: `schema.go` — add `authored_at` to `ddlTables` on both tables; rewrite the column comment (provenance only, no decay); double-quote any SQL comment inside the backtick literal
- [x] 3.5 Add `TestMigrate_V7toV8AddsAuthoredAtBackfillsCreatedAt` + a fresh-DB version=8 assertion to `migrations_test.go`
- [x] 3.6 GREEN: `save.go` — delete `decayHorizonDays`, `reviewAfterFor()`; drop `review_after` from the INSERT column list, `ON CONFLICT DO UPDATE`, and `forceUpdateConn`; keep `authored_at`/`resolveAuthoredAt`, rewrite their doc comments (provenance, no horizon)
- [x] 3.7 GREEN: `save.go` `pruneSessionSummariesConn` — add `AND scope = 'personal'` to the outer WHERE and the `NOT IN` subquery; add the fence sentence to the doc comment
- [x] 3.8 GREEN: `inspect.go` — add `Memory.AuthoredAt`; project in `GetRow` + `List` only
- [x] 3.9 GREEN: `share.go` — rewrite comments (drop decay framing); confirm export/import already carries `authored_at` (staged)
- [x] 3.10 GREEN: `internal/tui/view.go` `renderDetail` (line ~236) — one authorship line when `AuthoredAt != CreatedAt`; add a direct-call unit test
- [x] 3.11 Doc (gitignored, zero review lines): rewrite `docs/adr/0033-*.md` for Option 2; keep the ADR-0032 `ulid.Make()` factual correction; drop the ADR-0031 decay-horizon amendment; amend ADR-0028 for the retention fence + wire-format addition
- [x] 3.12 Gates: `go build ./cmd/droids-mem`, `go vet ./...`, `go test -count=1 ./...`, `golangci-lint run --timeout 5m`

## Rules carried into apply

- No `//nolint` pre-emptively — add only after the linter actually complains, with a reason.
- Backtick-terminates-raw-string trap: SQL comments inside `schema.go`'s backtick literals use double quotes, never backticks.
- Multi-`Save()` fixtures need genuinely dissimilar bodies (Jaccard ≥ 0.85 dedupe gate); direct-INSERT fixtures (`seedSummary`) bypass this.
