# Tasks: Partial Go Graph Index — Per-Package Degradation, Test Coverage, Dispatch Labels

## Chain Strategy: Feature Branch Chain (user-directed revision, supersedes the earlier single-PR recommendation)

```
develop
  └─ feat/graph-partial-index          (tracker, draft/no-merge until all 3 PRs approved)
       └─ PR1  (Phase 1 + Phase 2 + ordering fix pulled forward from Phase 6)
            └─ PR2  (Phase 3 + Phase 4 — indivisible core)
                 └─ PR3  (Phase 5 + Phase 6 remainder + Phase 7)
```

Only the tracker merges to `develop`, once, after all three PRs are reviewed and integrated. No intermediate state ever reaches `develop`.

## Review Workload Forecast

| PR | Phases | Production | Tests | Total | 400-line risk |
|---|---|---|---|---|---|
| PR1 | 1, 2, 6.1-6.2 (pulled forward) | ~57 | ~125 | ~182 | Low |
| PR2 | 3, 4 | ~200 | ~205 | ~405 | Medium (marginal — see note) |
| PR3 | 5 (+5.6/5.7), 6.3-6.10, 7 | ~198 | ~185 | ~383 | Low-Medium |
| **Total** | | **~455** | **~515** | **~970** | matches the earlier single-PR reforecast (~930-1030), now sliced |

Decision needed before apply: No — chain strategy and PR boundaries are explicit (user-directed).
Chained PRs recommended: Yes
Chain strategy: feature-branch-chain
400-line budget risk: Medium (PR2 only; PR1 and PR3 are Low)

**PR2 size note**: PR2 forecasts ~405 lines, marginally over the 400-line default. It cannot be split further without recreating the "no half is independently correct" problem at a finer grain: the closure precondition (3.1-3.2) exists only to guard the `prog.Build()` call added by the same `callEdges` rewrite (3.6); the >50%-broken cap (3.7-3.8) and closure-failure path (3.9-3.10) share one error-return path with that rewrite; `carry.go` (Phase 4) depends on the broken/kept partition and post-rewrite symbol ids Phase 3 produces. Recommend the maintainer accept PR2 as a soft `size:exception` (~400-430 lines, ~60-75 min review) rather than forcing a fourth PR — do not reopen this boundary purely to shave lines.

### Suggested Work Units

| PR | Goal | Targets | Focused test command | Runtime harness | Rollback boundary |
|---|---|---|---|---|---|
| PR1 | `Tests:true` + variant dedupe + stamp lockstep + production-first ordering guard + golden capture | tracker `feat/graph-partial-index` | `go test -count=1 ./internal/graph/... -run 'Dedupe|Stamp|Order|EdgeSetParity'` | `droids-mem serve --stdio` + `graph_symbol` on a repo with heavy test-caller volume (e.g. `store.Store.Save`); confirm production callers surface before test callers under the cap | revert PR1's commits; tracker returns to base `develop` state, nothing else to unwind |
| PR2 | Gate relaxation + closure precondition + `callEdges` rewrite (`DeleteSyntheticNodes` removed) + >50% cap + `carry.go` | PR1's branch | `go test -count=1 ./internal/graph/... -run 'Closure|Partition|Broken|Cap|Carr'` | `droids-mem serve --stdio` + `graph_symbol` on a repo with one injected type-erroring package; confirm symbols+edges still served for the rest of the tree | revert PR2's commits; PR1's branch (Tests:true+ordering) is untouched and still correct standalone |
| PR3 | `dispatch` schema + `Freshness.stale_units` + query/render splits + cap-independence + final gates | PR2's branch | `go test -count=1 ./internal/graph/... -run 'Dispatch|Freshness|Carried|Cap'` then full suite | `droids-mem serve --stdio` + `graph_symbol` full response inspection: dispatch split, `carried` flag, capped `stale_units` list | revert PR3's commits; PR2's carry-forward keeps working with the provisional `"static"` default it already shipped |

## Boundary Verifications (required by this revision)

### 1. Golden-capture split — verified meaningful, not vacuous

Phase 2's golden (`testdata/edges_clean.golden`) is captured in **PR1**, after Phase 1's `Tests:true`+dedupe land but while `cg.DeleteSyntheticNodes()` is still present (unchanged by Phase 1). The edge-set-equality test (2.2) is added in PR1 and passes trivially there — it only characterizes current behavior. `DeleteSyntheticNodes` is removed in **PR2** (task 3.6), inside `callEdges`'s rewrite. Because PR2 targets PR1's branch (chain topology), PR2 inherits both the golden file and the unmodified test from PR1's commit history; re-running that same test after 3.6 is what proves parity. **This split is meaningful**: the fix under test (removing `DeleteSyntheticNodes`) lands in a later commit on the same branch lineage as the pin, so the regression genuinely protects the removal rather than being satisfied by construction. Do not capture the golden any earlier (predates `Tests:true`, wrong symbol set) or any later (post-3.6, pins the wrong — already-changed — behavior).

### 2. Ordering fix pulled forward into PR1 — rationale (do not move back to Phase 6/PR3)

`Tests: true` (Phase 1, PR1) roughly triples caller counts immediately. The `ORDER BY is_test, (s.package != ?), s.qname` fix was originally scoped to Phase 6 (PR3). Shipping Phase 1 without it means same-package test callers sort ahead of cross-package production callers under the existing `(s.package != ?)`-only clause, so at the 50-neighbor cap an agent can see **zero production callers** and wrongly conclude a signature change is test-only — the exact hazard the ordering fix exists to prevent. That window would be live for the entire lifetime of PR1 and PR2 (both un-merged, but both individually reviewed and tested against real caller data) if left in Phase 6. The ordering change has no dependency of its own — it is a SQL `ORDER BY` clause on the existing `neighborLevel` query plus its RED test, using only the pre-existing `file` column (no schema change, no dependency on `dispatch` or `carried_units`) — so it was moved into PR1 as tasks **6.1 (RED)** and **6.2 (GREEN)**, executed and reviewed as part of Phase 1/2's PR, ahead of their listing under Phase 6 below. **Do not reorder this back into PR3.**

### 3. Dispatch-column dependency in carry-forward — boundary corrected, flagged explicitly

The originally-planned task 4.1 included a "previous `graph.db` predates the `dispatch` column → `SELECT e.dispatch` fails → zero carried edges" scenario inside PR2 (Phase 4). **This does not hold at the PR2 boundary as originally planned**: the `dispatch` schema column is added in Phase 5 (PR3), so during PR2's entire lifetime no `graph.db` in existence — old or freshly built by PR2 itself — has that column. If `carriedEdges` selected `dispatch` in PR2, the "no such column" branch would fire on *every* carry-forward attempt, silently defeating carry-forward's actual purpose (carrying real edges) for the whole PR2 branch. **Corrected boundary**: in PR2, `carriedEdges` selects only `caller, callee` from the previous graph and defaults each carried edge's dispatch to `"static"` (consistent with the existing nil-`Site`→`"static"` convention) — carry-forward genuinely works in PR2, just without a real dispatch label for carried edges yet. `callEdges` (3.6, also PR2) still computes correct real dispatch labels for **fresh** edges; only persistence and the carried-edge dispatch preservation move to PR3. New tasks **5.6 (RED)** / **5.7 (GREEN)** in Phase 5 upgrade `carriedEdges` to select+preserve real `dispatch` once the column exists, and that is where the "predates dispatch column" defensive test correctly belongs (it protects PR3+ from carrying forward a PR1/PR2-era graph, which is the only scenario where that column can actually be missing). Task 4.1's scope below is updated accordingly.

## Phase 1: Foundation — `Tests: true`, variant dedupe, stamp census lockstep — **[PR1]**

These three land in one commit: `Tests:true` alone duplicates every symbol row (dedupe missing), and editing a test file won't move the stamp unless `stamp.go:97`'s exclusion drops in the same change.

- [x] 1.1 RED `index_test.go`: fixture with plain+in-package-test variant under `Tests:true` — assert no duplicate `qname`, `findSymbol` resolves unambiguously.
- [x] 1.2 GREEN `index.go`: add `Tests: true` to `packages.Config`; add `dedupeVariants(pkgs)` — keep the `" ["`-variant per `PkgPath` when present else the plain one, skip synthesized `<pkg>.test` main; SSA still created for every variant later (Phase 3).
- [x] 1.3 RED `stamp_test.go`: editing a `_test.go` file changes `stamp()` output.
- [x] 1.4 GREEN `stamp.go`: drop the `_test.go` exclusion at `:97`; update the comment.

## Phase 2: Golden capture — ORDER-CRITICAL, run only after Phase 1 lands — **[PR1]**

- [x] 2.1 With Phase 1 active and `cg.DeleteSyntheticNodes()` still present, generate `testdata/edges_clean.golden`: sorted `(caller_qname,callee_qname)` pairs from the clean `testdata/testmod` build. See "Boundary Verifications §1" — captured any earlier or later pins the wrong behavior.
- [x] 2.2 `build_test.go`: add edge-set-equality test comparing a fresh build's pairs to the golden — passes now; re-verified unchanged in PR2 (3.6) to prove parity after `DeleteSyntheticNodes` removal.

## Phase 3: Partial-build core — partition, closure precondition, `callEdges` rewrite — **[PR2]**

- [x] 3.1 RED `index_test.go`: `assertImportClosure` direct-call — closure holds (nil error); closure violated via a hand-built created-set missing a reachable import (non-nil error). Never calls `prog.Build()` on the incomplete set.
- [x] 3.2 GREEN: implement `assertImportClosure(prog *ssa.Program, created []*packages.Package) error`.
- [x] 3.3 RED: `copyFixture`+`writeInto`-injected type error — symbol rows still emitted for the broken package from AST.
- [x] 3.4 GREEN: replace the all-or-nothing gate (`index.go:63-67`) with partition into `pkgSet{kept, broken}` by `len(p.Errors)>0`; run `appendDeclSymbols` over every kept package.
- [x] 3.5 RED: broken-package in-edges survive (called by a clean package, not dropped).
- [x] 3.6 GREEN: rewrite `callEdges` — `packages.Visit(pkgs, nil, create)`, `create` does `prog.CreatePackage(p.Types, p.Syntax, p.TypesInfo, true)` for clean / `(p.Types, nil, nil, true)` stub for broken / skip `p.Types==nil`; call `assertImportClosure`; then `prog.Build()`, `cha.CallGraph`; remove `cg.DeleteSyntheticNodes()` unconditionally; collect real `dispatch` per fresh edge (nil-safe) into `edgeSet map[[2]int64]string` — **computed and unit-tested here, but not yet persisted** (schema column lands Phase 5/PR3; `writeGraphDB`'s edge INSERT stays 2-column, ignoring the map value). Re-run Phase 2.2's golden test — must still pass.
- [x] 3.7 RED: >50% broken packages — no fresh `graph.db` written, previous served, `Freshness.Stale: true`.
- [x] 3.8 GREEN: add the >50%-broken check before symbol/SSA work, distinct error return — reuses `buildAsync`'s existing stale+`IndexError` path.
- [x] 3.9 RED: closure-precondition failure produces the byte-identical served state as 3.7/3.8.
- [x] 3.10 GREEN: wire `assertImportClosure` failure into the same error return as 3.8.

## Phase 4: Carry-forward (`carry.go`, new) — **[PR2]**

- [x] 4.1 RED `carry_test.go`, hand-seeded prev-db fixtures: caller-in-broken-package edge carried + id-remapped by qname; caller-qname-miss drops the edge; cross-unit (clean caller→broken callee) never carried; no previous graph → zero carried, build succeeds. *(The "predates the `dispatch` column" scenario moved to 5.6 — see Boundary Verifications §3; it is not reachable from PR2's code path.)*
- [x] 4.2 GREEN: create `carry.go` — `openPrevGraph(dbPath)` (`sql.Open("sqlite","file:"+dbPath+"?mode=ro")`, same pattern as `buildlock.go:117` `stampOnDisk`); `carriedEdges(dbPath string, broken map[string]bool, byQName map[string]int64) (edgeSet, error)`, best-effort — any open/query/scan error ⇒ `(nil, nil)`. Selects only `caller, callee` (no `dispatch` column yet); every carried edge defaults to `"static"` pending 5.6/5.7. Open once per `buildIndex`, `defer db.Close()` at function scope, only when `len(broken)>0`.
- [x] 4.3 GREEN: wire into `buildIndex` — splice `carriedEdges` output into the fresh edge map after 3.6; collect carried package names into `carried_units`. *(Boundary note: `carried_units` is computed as a local `brokenPkgNames` set inside `buildIndex` for the splice keying, but not threaded to any consumer in PR2 — `Freshness.StaleUnits`/`meta.carried_units` persistence is PR3-only per this PR's explicit scope boundary, and the project convention against speculative unused fields ruled out adding a dead-code carrier for it here. PR3's 5.1-5.3 will source it from the same broken-package-name computation.)*

## Phase 5: Schema + Freshness (`graph.go`) — **[PR3]**

- [x] 5.1 GREEN: `schema` const gains `edges.dispatch TEXT NOT NULL DEFAULT 'static'` — this DDL is what moves `stampGen`/`currentGen` for the one-time cache invalidation; `carried_units` is a meta row and does NOT independently invalidate.
- [x] 5.2 GREEN: `writeGraphDB` writes per-edge `dispatch` (now persisting the real labels 3.6 already computes) and `meta.carried_units`.
- [x] 5.3 GREEN: `open()` reads `meta.carried_units`; `Freshness` gains `StaleUnits []string` (first 5) + `StaleUnitsTotal int`; `Stale` narrows to genuine build failures (I/O, corrupt cache) only.
- [x] 5.4 RED: 213 carried units → response lists first 5 + total 213 + hint, not inlined.
- [x] 5.5 RED: partial build that itself succeeds → `Freshness.Stale == false`.
- [x] 5.6 RED `carry_test.go`: seed a pre-5.1-schema prev db (no `dispatch` column) — `carriedEdges` read fails with "no such column: dispatch", yields zero carried edges, build still succeeds. (Moved from 4.1 — see Boundary Verifications §3; only meaningful once `carriedEdges` selects `dispatch`, below.)
- [x] 5.7 GREEN: upgrade `carriedEdges`'s SELECT to include `dispatch`, preserving real carried-edge labels now that the column exists; keeps the best-effort any-error-⇒-zero-carried contract from 4.2.

## Phase 6: Query + render splits (`query.go`, `render.go`)

`6.1`-`6.2` ship in **PR1** (pulled forward — see Boundary Verifications §2). `6.3`-`6.10` ship in **PR3**.

- [x] 6.1 RED `query_test.go` **[PR1]**: production callers precede test callers under a cap smaller than total; same-package proximity preserved within each `is_test` group.
- [x] 6.2 GREEN **[PR1]**: `neighborLevel` `ORDER BY` → `is_test, (s.package != ?), s.qname`; `is_test` via `s.file LIKE '%\_test.go' ESCAPE '\'`.
- [x] 6.3 RED **[PR3]**: 86 test callers over 19 distinct files → correct `CallersInTests`/`CallerTestFiles`.
- [x] 6.4 GREEN **[PR3]**: add `CallersInTests`, `CallerTestFiles`, `CallersViaInterface`, `Carried` to `SymbolResponse`; SQL computes all at depth=1, cap-independent.
- [x] 6.5 RED **[PR3]**: dispatch hint fires at 36/40 (90%) interface, not at 10/40 (25%).
- [x] 6.6 GREEN **[PR3]**: named `dispatchHintRatio = 0.5`; hint fires when interface/total > ratio.
- [x] 6.7 RED **[PR3]**: caller-list response carries no per-row test/dispatch field — only response-level splits.
- [x] 6.8 GREEN **[PR3]** (covers 6.7 + finishes 6.4/6.6): `render.go` — render splits, `stale_units[N of M]` + hint, `carried` flag; reword `staleGraphHint`/freshness copy ("no longer type-checks" is now false for a partial build).
- [x] 6.9 RED **[PR3]**: symbol in a carried-edge package → `carried:true`; symbol in a cleanly-built package → `carried:false`.
- [x] 6.10 GREEN **[PR3]**: derive `Carried` from the `carried_units` set (5.3).

*(6.1/6.2 marked `[x]` above only to flag "already scheduled/executed as part of PR1" in this task-tracking doc — do not treat as pre-completed by `sdd-apply`; PR1's own checklist state governs actual completion.)*

## Phase 7: Cap invariance + verification — **[PR3]**

- [x] 7.1 RED: ordering, all split counts, `CallersTotal`/`CalleesTotal`, `Truncated` are cap-independent; only the shown slice length changes with `maxNeighbors`.
- [x] 7.2 Confirm `maxNeighbors` stays the single `var` at `query.go:53`, no new value-dependent branch.
- [x] 7.3 `go build ./cmd/droids-mem`, `go test -count=1 ./...`, `go vet ./...`, `golangci-lint run --timeout 5m` — all clean (full-suite final gate, superset of each PR's own gate run below).
- [x] 7.4 Update `docs/adr/0034-*.md` marking inventory 4 / 7a / dispatch landed (gitignored, zero review lines).

## Per-PR Gate Verification

| PR | `go build ./cmd/droids-mem` | `go test -count=1 ./...` | `go vet ./...` | `golangci-lint run --timeout 5m` | Why it's independently green |
|---|---|---|---|---|---|
| PR1 | Green | Green | Green | Green | `Tests:true`+dedupe only touches `packages.Config` + a pure new function; the ordering fix is a self-contained `ORDER BY` change on an existing query, no schema/type change; golden file + regression test only observe existing, untouched `callEdges` behavior. No code path in PR1 depends on PR2/PR3. |
| PR2 | Green | Green | Green | Green | `callEdges` rewrite, `assertImportClosure`, and `carry.go` are self-contained within `internal/graph`; `dispatch` is computed and typed (`edgeSet`) but not persisted — `writeGraphDB`'s edge INSERT is untouched (still 2-column), so the unused string value is not a compile error. The golden test inherited from PR1 is re-verified green post-rewrite, proving `DeleteSyntheticNodes` removal is safe. |
| PR3 | Green | Green | Green | Green | Adds the `dispatch` schema column and starts persisting values PR2 already computes; upgrades `carriedEdges`'s SELECT to include `dispatch` (5.6/5.7 — now meaningful, since only PR1/PR2-era graphs lack the column); layers the query/render surface on stable Phase 3/4 internals. Final full-suite gate run (7.3) is the superset check. |
