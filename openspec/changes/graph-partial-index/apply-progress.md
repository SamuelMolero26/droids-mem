# Apply progress: graph-partial-index — PR1 + PR2 + PR3

## PR3 (this entry) — Phase 5 (+5.6/5.7), Phase 6 tasks 6.3-6.10, Phase 7

Scope: Phase 5 (all, including 5.6/5.7), Phase 6 tasks 6.3-6.10, and Phase 7
(all). Tasks 6.1/6.2 already shipped in PR1, not redone here.

Branch: `feat/graph-partial-index-pr3`, branched from
`feat/graph-partial-index-pr2` (verified via `git branch --show-current`
before any edit; PR1+PR2 work is already in this branch's history).

### Tasks completed

- [x] 5.1 GREEN `graph.go` — `schema`'s `edges` table gains `dispatch TEXT NOT NULL DEFAULT 'static'`
- [x] 5.2 GREEN `index.go` — `writeGraphDB` gains a `carriedUnits []string` param; edge INSERT is now 3-column (`caller,callee,dispatch`); `meta.carried_units` written (newline-joined short package names); `buildIndex` now collects `carriedUnits` unconditionally from `set.broken` (not just when `carriedEdges` recovers something — matches design.md's "this unit's edges are not freshly analyzed" semantics)
- [x] 5.3 GREEN `graph.go` — `Freshness` gains `StaleUnits []string` (capped at `staleUnitsCap = 5`) + `StaleUnitsTotal int` + an unexported `carriedUnits map[string]bool` (full set, never serialized — backs the per-symbol `Carried` flag, since membership can fall outside the capped `StaleUnits` list); `open()`'s meta SELECT widened to include `carried_units` and populates all three
- [x] 5.4 RED `freshness_test.go` (new file) — `TestOpen_CapsStaleUnitsAtFive`: 213 hand-seeded units → `StaleUnits` has exactly the first 5, `StaleUnitsTotal == 213`
- [x] 5.5 RED `freshness_test.go` — `TestPartialBuildSucceeds_NotStale`: a real 1-of-2-broken partial build (via `m.Index`, synchronous) must report `Freshness.Stale == false` once it lands
- [x] 5.6 RED `carry_test.go` — `TestCarriedEdges_OldSchemaPrevGraphYieldsZeroCarried`: seeds a pre-5.1-schema prev db (raw DDL literal, no `dispatch` column) — `carriedEdges` must swallow the "no such column: dispatch" failure into zero edges, no error
- [x] 5.7 GREEN `carry.go` — `carriedEdges`'s SELECT now includes `e.dispatch`, preserving the real label instead of hardcoding `"static"` for every carried edge; `TestCarriedEdges_PreservesRealDispatchLabel` (new) proves an `"interface"`-labelled carried edge survives with its real label, not overwritten
- [x] 6.3 RED `query_test.go` — `TestCallerSplit_ProductionTestAndDistinctFiles`: zz.Hub with 2 production + 3 test callers across 2 distinct `_test.go` files — `CallersInTests`/`CallerTestFiles` must be independent numbers (3 vs 2)
- [x] 6.4 GREEN `query.go` — `SymbolResponse` gains `CallersInTests`, `CallerTestFiles`, `CallersViaInterface`, `Carried`; new `callerSplit(ctx, conn, id)` computes all three split counts (+ the true total) in one SQL pass over `edges JOIN symbols`, independent of `maxNeighbors` and of the request's own `Depth`; wired into `Symbol()`'s `dir=="up"||"both"` branch; `Carried` set from `fresh.carriedUnits[info.Package]` right after `resp.Symbol` is populated
- [x] 6.5 RED / 6.6 GREEN `query_dispatch_test.go` (new file) + `testdata/dispatch` (new fixture, own `go.mod` — module-anchoring requires it, same as `testdata/fanout`/`testdata/closures`) — `TestCallersViaInterface_SplitAndDominanceHint`: `Dominant.Do` (3 interface + 1 static = 75%) fires `dispatchDominanceHint`; `Minor.Do` (3 interface + 6 static = 33%, sharing the same 3 interface call sites via CHA fan-out) does not. `dispatchHintRatio = 0.5` named constant in `query.go`.
- [x] 6.7 RED `query_dispatch_test.go` — `TestNeighbor_NoPerRowTestOrDispatchField`: reflection over `Neighbor`'s json tags + a real JSON round-trip, asserting no per-row test/dispatch/origin field exists. Passed on first write — `Neighbor` already carried no such field (characterization/regression guard, not a defect fix); kept as a permanent regression test per the spec's explicit "no per-row field" requirement.
- [x] 6.8 GREEN `render.go` — `RenderSymbol` now emits `callers_in_tests`/`caller_test_files`/`callers_via_interface`/`carried` lines; `writeFreshness` reworked: the `STALE` message only claims "repo no longer type-checks" when `IndexError` is actually present (a bare `Stale && !IndexError` — a benign in-flight async rebuild — now reads "serving the previous graph while an update is pending"); adds `stale_units[N of M]: <names> (<hint>)` when `StaleUnits` is non-empty, using the new `staleUnitsHint` constant
- [x] 6.9 RED / 6.10 GREEN — `TestSymbol_CarriedFlag` (`freshness_test.go`): a real 1-of-2-broken partial build, `zz.Hub` (in the broken/carried package) → `Carried:true`; `Announce` (in the cleanly-built package) → `Carried:false`. Implementation is the one-line `fresh.carriedUnits[info.Package]` lookup added in 6.4.
- [x] 7.1 RED `query_dispatch_test.go` — `TestCapInvariance_SplitsAndTotalsIndependentOfCap`: `dispatch.Minor.Do` (9 total callers) queried at `maxNeighbors=2` and `maxNeighbors=9` — `Truncated`, `CallersTotal`, and all three caller-fidelity splits are identical across both cap values; only `len(Callers)` differs (2 vs 9). Passed on first write (see "Deviations" below).
- [x] 7.2 Verified by inspection: `maxNeighbors` is still the single `var` at `query.go` (now line ~70, shifted by the new consts above it); no new code branches on its specific value — every use is a `>=`/`>` comparison against slice length, same shape as before this PR.
- [x] 7.3 All four gates run clean from repo root — see "Gates" below.
- [x] 7.4 `docs/adr/0034-multi-language-code-graph.md` updated (gitignored, untracked by git, zero review lines): a new 2026-08-15 revision-note paragraph marking inventory item 4 (`buildIndex` restructure), decision 7a (`Tests: true`), and the `dispatch` column as landed; inventory items 2 and 4 individually annotated with a "Landed"/"Partially landed" note (item 2's `precision`/`imports` table are explicitly still unbuilt — only `dispatch`+`stale_units` shipped).

### RED failure messages (actual)

5.1-5.3 (`TestBuildIndex_PersistsDispatchLabel`, before the schema/writeGraphDB change):
```
index_test.go:298: edge testmod.Announce -> testmod.pick: SQL logic error: no such column: e.dispatch (1)
```
(`TestBuildIndex_PersistsCarriedUnits`, same commit, before `meta.carried_units` was written):
```
index_test.go:346: meta.carried_units: sql: no rows in result set
```
(`TestOpen_CapsStaleUnitsAtFive`/`TestOpen_NoCarriedUnitsIsZeroValue`/`TestPartialBuildSucceeds_NotStale`, before the `Freshness` fields existed — build failure, not a runtime failure):
```
internal/graph/freshness_test.go:61:11: fresh.StaleUnitsTotal undefined (type Freshness has no field or method StaleUnitsTotal)
internal/graph/freshness_test.go:64:15: fresh.StaleUnits undefined (type Freshness has no field or method StaleUnits)
... (10 total undefined-field errors)
FAIL	github.com/samuelmolero26/droids-mem/internal/graph [build failed]
```

5.6/5.7 (`TestCarriedEdges_OldSchemaPrevGraphYieldsZeroCarried` / `TestCarriedEdges_PreservesRealDispatchLabel`, before `carriedEdges` selected `dispatch`):
```
carry_test.go:204: want 0 carried edges reading dispatch from a pre-dispatch-column graph.db, got map[[501 502]:static]
carry_test.go:250: carried edge dispatch = "static", want the preserved real label "interface"
```
(5.6's RED is inverted from the usual shape: the OLD-schema test actually PASSED trivially before 5.7's SELECT-widening — carriedEdges wasn't reading a `dispatch` column yet, so there was nothing to fail against. The real defect these two tests jointly pin only exists together: 5.7's SELECT widening is what makes 5.6's guard meaningful, exactly as the task doc's own boundary note states. 5.7's own test (`PreservesRealDispatchLabel`) is the one that failed for the stated reason above before the GREEN.)

6.3 (`TestCallerSplit_ProductionTestAndDistinctFiles`, 6.4 (`TestCallersViaInterface_SplitAndDominanceHint`), 6.9 (`TestSymbol_CarriedFlag`) — all failed identically as compile errors before their fields existed:
```
internal/graph/query_test.go:83:10: resp.CallersInTests undefined (type *SymbolResponse has no field or method CallersInTests)
internal/graph/query_dispatch_test.go:28:10: resp.CallersViaInterface undefined (type *SymbolResponse has no field or method CallersViaInterface)
internal/graph/query_dispatch_test.go:31:34: undefined: dispatchDominanceHint
internal/graph/freshness_test.go:161:11: resp.Carried undefined (type *SymbolResponse has no field or method Carried)
```

6.8 (`TestRenderSymbol_CallerSplitsAndCarried` / `TestRenderSymbol_StaleUnitsCappedWithHint` / `TestRenderSymbol_StaleWordingNotClaimedWithoutFailure`, before the `render.go` changes):
```
render_test.go:79: missing callers_in_tests: ...
render_test.go:107: missing capped stale_units header: ...
render_test.go:129: claimed a type-check failure with no IndexError present:
    freshness: STALE (repo no longer type-checks; serving last good index); REBUILDING (async rebuild in progress)
```

### Characterization tests (passed on first write, not a defect fix)

Per the "a test passing on first write is a red flag — investigate" instruction, each of these was written, run, found already-GREEN, and investigated before being kept as a permanent regression guard rather than treated as a false RED cycle:

- **5.5 `TestPartialBuildSucceeds_NotStale`**: `Freshness.Stale` is only ever set `true` by `ensureFresh` on the warm-serve/failed-stamp paths; a synchronous `m.Index` call against a build that succeeds always lands with `fresh.Stamp == current`, so `Stale` was already correctly `false` by construction — the existing `ensureFresh`/`open()` logic already satisfied the "partial success is not stale" invariant before this PR touched anything. Kept as the requirement's own regression test (spec explicitly lists this scenario).
- **6.7 `TestNeighbor_NoPerRowTestOrDispatchField`**: `Neighbor` never gained a per-row field in this PR (by design — the whole point of `callerSplit`/`dispatch` being response-level). The reflection+JSON-round-trip check simply confirms that design decision holds, both now and against future regressions.
- **7.1 `TestCapInvariance_SplitsAndTotalsIndependentOfCap`**: `callerSplit`/`edgeCount` are both SQL queries against the full `edges` table, never against the capped `Callers` slice — cap-independence was true by construction of how they were written in 6.4/pre-existing `edgeCount`, not something that needed a separate fix. Kept as the explicit Phase 7 invariance gate the design calls for.

### Deviation: `testdata/dispatch` needed its own `go.mod`

Discovered via a failing first draft of `TestCallersViaInterface_SplitAndDominanceHint` (0 callers found, not the expected count). Root cause: `canonicalRepo`/`moduleRoot` anchors on the nearest ancestor `go.mod`, and a brand-new `testdata/dispatch/` directory with no `go.mod` of its own resolved all the way up to the droids-mem repo root's `go.mod` — the test was accidentally indexing the entire droids-mem tree (1267 symbols) instead of the 17-line fixture. Fixed by adding `testdata/dispatch/go.mod` (`module example.com/dispatch`), matching the existing `testdata/fanout`/`testdata/closures`/`testdata/testmod` convention — every Go fixture directory under `internal/graph/testdata/` needs its own `go.mod` for exactly this reason, which is easy to miss when adding a new one.

### Gates (all run from repo root, real output)

- `go build ./cmd/droids-mem` → exit 0, no output
- `go test -count=1 ./...` → all packages `ok` (cmd/droids-mem, internal/db,
  internal/graph, internal/mcpserver, internal/scrub, internal/share,
  internal/state, internal/store, internal/tui)
- `go test -race -shuffle=on ./...` → all packages `ok` (run twice — once
  before, once after the gofmt fix below — both clean)
- `go vet ./...` → exit 0, no output
- `golangci-lint run --timeout 5m` → one round-trip: `gofmt` flagged
  `query.go` (a `const` block's alignment shifted after the new
  `dispatchDominanceHint`/`staleUnitsHint` entries were added — `gofmt -w`
  realigns column widths across the whole block); re-ran, `0 issues.`

### Line split (git diff --numstat / wc -l on new files)

Production (existing files, `git diff --numstat`):
- `internal/graph/graph.go`: +37/-4 (`dispatch` DDL column, `Freshness.StaleUnits`/`StaleUnitsTotal`/`carriedUnits`, `open()` meta parsing)
- `internal/graph/index.go`: +26/-8 (`writeGraphDB` signature + `carriedUnits` param, 3-column edge INSERT, `meta.carried_units` write, `buildIndex`'s unconditional `carriedUnits` collection)
- `internal/graph/carry.go`: +13/-12 (SELECT widened to `e.dispatch`, preserved instead of hardcoded)
- `internal/graph/query.go`: +68/-6 (new consts, `SymbolResponse` fields, `callerSplit`, `Carried`/hint wiring, reworded `staleGraphHint`)
- `internal/graph/render.go`: +33/-8 (caller-split + `carried` render lines, reworked `writeFreshness`)

Production total: **+177/-38** (git diff, existing files only)

Fixture (new, not Go test code — same category as PR1's golden file):
- `internal/graph/testdata/dispatch/dispatch.go`: 55 lines
- `internal/graph/testdata/dispatch/go.mod`: 3 lines

Tests (existing files, `git diff --numstat`):
- `internal/graph/carry_test.go`: +129/-1 (5.6/5.7 tests + old-schema seed helper + dispatch-preservation test)
- `internal/graph/graph_test.go`: +1/-1 (`writeGraphDB` call site: 9th `carriedUnits` param)
- `internal/graph/index_test.go`: +91/-0 (dispatch-persistence + carried-units-persistence tests)
- `internal/graph/query_test.go`: +39/-0 (caller-split production/test/file-count test)
- `internal/graph/render_test.go`: +74/-0 (caller-split/carried render test, stale_units cap render test, reworded-STALE-wording test)

Tests (new files):
- `internal/graph/freshness_test.go`: 172 lines (stale-units cap, no-carried-units zero value, partial-build-not-stale, carried-flag)
- `internal/graph/query_dispatch_test.go`: 136 lines (dispatch split/hint, no-per-row-field, cap-invariance)

Test total: **+642/-2** (334 in existing files + 308 new files)

Grand total: production ~177 added / 38 removed, tests ~642 added / 2
removed, fixture 58 lines — **tests are well over PR3's own ~185-line
forecast** (production is comfortably under the ~198 forecast). Same pattern
as PR2: the overage is concentrated in tests because Phase 5-7 introduced 4
genuinely new response fields (`CallersInTests`, `CallerTestFiles`,
`CallersViaInterface`, `Carried`) plus a capped-list field
(`StaleUnits`/`StaleUnitsTotal`) and a reworded freshness-text contract, each
needing its own RED/GREEN pair plus a dedicated fixture (`testdata/dispatch`)
to get a controllable interface/static dispatch ratio without disturbing the
golden-pinned `testdata/testmod` tree. No test was cut to fit the budget.
Flagging for maintainer awareness, not treating it as a failure — this
mirrors PR2's explicitly-accepted overage pattern.

### Risks / deviations

- **Tests exceed PR3's own forecast** (~642 vs ~185 lines) — see line-split
  note above. Production (~177) is under its ~198 forecast.
- No droids-mem MCP tools available to this sub-agent session — only Engram
  was used. Orchestrator is expected to mirror into droids-mem per the
  dual-persist convention; flagging the gap per instructions, not treating it
  as a failure.
- `testdata/dispatch` is a new fixture directory (with its own `go.mod`,
  matching the existing per-fixture-module convention) — not reused from an
  existing fixture, because none of `testdata/testmod`/`fanout`/`closures`
  could produce a controllable interface-vs-static dispatch ratio on the same
  callee symbol without either disturbing `testdata/testmod`'s golden-pinned
  edge set or overloading `fanout`'s existing fan-out-measurement purpose.
- `writeFreshness`'s STALE wording change (6.8) affects EVERY `Freshness`
  render with `Stale && IndexError == ""` — i.e., every ordinary benign
  in-flight async rebuild across the whole graph subsystem, not just the
  partial-build scenarios this PR is scoped to. This is the correct fix per
  design.md's explicit instruction ("reword staleGraphHint/writeFreshness
  copy") and no existing test asserted the old wording's literal text (only
  `strings.Contains(out, "freshness: STALE")` and the presence of
  `IndexError`'s own text), so nothing broke — flagging the wider blast
  radius for visibility since it is a user(agent)-facing text change beyond
  this PR's own new features.
- `internal/mcpserver/server.go` and `graph_tools.go` still contain doc
  strings claiming "the repo no longer type-checks" for any `stale: true` —
  the same overclaim `writeFreshness` (render.go) and `staleGraphHint`
  (query.go) just fixed. design.md's task 6.8 scoped the reword to
  `staleGraphHint`/`writeFreshness` specifically, not the MCP tool
  descriptions, so left untouched here — flagging as a candidate follow-up,
  not fixed under this PR's explicit scope.
- Untouched by design and verified still present/unmodified: `precision`
  column, `imports` table, and everything else under ADR-0034 inventory item
  5 (the mapper tier) — this PR closes only the Go-only slice (inventory
  item 4, decision 7a, the `dispatch` column) that the ADR calls out as
  "independently shippable".
- `openspec/` remains untracked in git per repo convention; nothing staged or
  committed. `docs/adr/0034-multi-language-code-graph.md` was edited (task
  7.4) but is itself gitignored (`/docs/` — confirmed via
  `git check-ignore -v`), so this edit is invisible to `git status` and carries
  zero review-diff weight, per the task's own "gitignored, zero review lines"
  framing.

## PR2 (this entry) — Phase 3 + Phase 4

Scope: Phase 3 (all tasks) and Phase 4 (all tasks) only. Nothing from Phase 5,
6.3-6.10, or 7 touched (`edges.dispatch` schema column, `Freshness.StaleUnits`,
the `carried` flag, query/render surface work all untouched).

Branch: `feat/graph-partial-index-pr2`, branched from `feat/graph-partial-index-pr1`
(verified via `git branch --show-current` before any edit; PR1's work is
already in this branch's history).

### Tasks completed

- [x] 3.1 RED `index_test.go` — `assertImportClosure` direct-call: closure holds (nil), closure violated via hand-built created-set (non-nil), never calls `prog.Build()`
- [x] 3.2 GREEN `index.go` — `assertImportClosure(prog *ssa.Program, created []*packages.Package) error`, walks `p.Imports` transitively, checks `prog.Package(p.Types) != nil`
- [x] 3.3 RED `index_test.go` — broken-package type error via `copyFixture`+`writeFile`: symbol rows still emitted from AST
- [x] 3.4 GREEN `index.go` — `pkgSet{kept, broken}` + `partition(pkgs)` replaces the all-or-nothing gate; `kept` = `dedupeVariants` output (every package, broken or clean); `broken` = raw (non-deduped) `len(p.Errors)>0` subset
- [x] 3.5 RED `index_test.go` — clean-caller-into-broken-package in-edge must survive (`testmod.main -> zz.Hub`)
- [x] 3.6 GREEN `index.go` — `callEdges` rewritten: `packages.Visit(pkgs, nil, create)` + manual `prog.CreatePackage` (clean=full syntax, broken=types-only stub, `p.Types==nil`=skip); `assertImportClosure` before `prog.Build()`; `cg.DeleteSyntheticNodes()` removed unconditionally; edges now typed `edgeSet map[[2]int64]string` with real per-edge dispatch (`"interface"` iff `e.Site != nil && e.Site.Common().IsInvoke()`, else `"static"`) — computed but NOT persisted (`writeGraphDB`'s edge INSERT stays 2-column, unchanged). Golden parity test (`TestCallEdges_MatchesCleanGoldenEdgeSet`, inherited from PR1) re-verified green post-removal.
- [x] 3.7 RED `build_test.go` — `TestBuildIndex_MajorityBrokenServesPreviousGraph`: >50% broken (both of testmod's 2 packages) must not write a fresh graph.db
- [x] 3.8 GREEN `index.go` — `len(set.broken)*2 > len(pkgs)` check right after `partition`, before any symbol/SSA work; error text keeps the pre-existing `"repo does not type-check"` prefix so legacy failure-path assertions keep working unmodified
- [x] 3.9 RED / 3.10 GREEN `build_test.go` — `TestClosurePreconditionFailure_SameServedStateAsMajorityBroken`: verifies the downstream parity (Manager-level bookkeeping, served response shape) between a closure-precondition failure and the >50% cap. See "Deviation" below for why this is NOT an organic end-to-end trigger.
- [x] 4.1 RED `carry_test.go` (new file) — hand-seeded prev-graph.db fixtures via the `schema` const + raw INSERTs: caller-in-broken-package carried+remapped, caller-qname-miss dropped, cross-unit never carried, no-previous-graph yields zero carried without error
- [x] 4.2 GREEN `carry.go` (new file) — `openPrevGraph(dbPath)` (same `sql.Open(...?mode=ro)` pattern as `buildlock.go`'s `stampOnDisk`); `carriedEdges(dbPath, brokenPkgs map[string]bool, byQName map[string]int64) (edgeSet, error)`; every branch returns literal `(nil, nil)` on failure (best-effort by contract); selects only `caller,callee` + `s1.package`, defaults every carried edge to `"static"` (PR2 boundary — no `dispatch` column yet)
- [x] 4.3 GREEN `index.go` — wired into `buildIndex` after `callEdges`: builds `brokenPkgNames` (short pkg names from `set.broken`) and `byQName` (from freshly-assigned `symbols` ids), calls `carriedEdges(dbPath, ...)`, splices results into `edges` unconditionally (no key collision is possible: a carried edge's caller is always in a broken package, which can never appear as a caller in the freshly-computed `edges` map since that package's functions have no SSA body). End-to-end proof: `TestBuildIndex_CarryForwardRecoversBrokenPackageInternalEdge` (`index_test.go`) calls `buildIndex` twice against the SAME `dbPath` — exactly the production shape — and confirms `zz.Near -> zz.Hub` (a caller-inside-the-broken-package edge, provably undiscoverable by `callEdges` alone per 3.5's test) reappears only via carry-forward.

### RED failure messages (actual)

3.1/3.2 (`TestAssertImportClosure_Holds`/`_Violated`, before `assertImportClosure` existed):
```
internal/graph/index_test.go:121:12: undefined: assertImportClosure
internal/graph/index_test.go:149:9: undefined: assertImportClosure
FAIL	github.com/samuelmolero26/droids-mem/internal/graph [build failed]
```

3.3 (`TestBuildIndex_BrokenPackageStillYieldsSymbols`, before partition):
```
index_test.go:182: buildIndex must partition around a single body-local type error, got: repo does not type-check: .../testmod/zz/zz_broken.go:4:14: cannot use "this does not type-check" (untyped string constant) as int value in variable declaration
```

3.5 (`TestBuildIndex_BrokenPackageInEdgesSurvive`, before the `callEdges` rewrite — `ssautil.AllPackages` still filtering transitively on `IllTyped`):
```
index_test.go:251: edge testmod.main -> zz.Hub: got 0, want 1 (in-edge into a broken package must survive)
index_test.go:251: edge zz.Near -> zz.Hub: got 0, want 1 (in-edge into a broken package must survive)
```
(Second assertion, `zz.Near -> zz.Hub`, was subsequently REMOVED from this test — see "Deviation" below; it was never a valid target for 3.5/3.6, only for carry-forward.)

3.7 (`TestBuildIndex_MajorityBrokenServesPreviousGraph`, before the >50% cap):
```
build_test.go:445: a majority-broken build must not write a fresh graph.db
```
(Build succeeded and overwrote graph.db — no cap existed yet.)

4.1 (`TestCarriedEdges_*`, before `carry.go` existed):
```
internal/graph/carry_test.go:67:14: undefined: carriedEdges
internal/graph/carry_test.go:96:14: undefined: carriedEdges
internal/graph/carry_test.go:121:14: undefined: carriedEdges
internal/graph/carry_test.go:139:14: undefined: carriedEdges
FAIL	github.com/samuelmolero26/droids-mem/internal/graph [build failed]
```

4.3 (`TestBuildIndex_CarryForwardRecoversBrokenPackageInternalEdge`, before wiring `carriedEdges` into `buildIndex`):
```
index_test.go:315: edge zz.Near -> zz.Hub: got 0, want 1 (must be carried forward from the previous graph.db)
```

### Deviations from the literal task text (both flagged in the corresponding task's inline note)

1. **3.5's original test asserted a second edge (`zz.Near -> zz.Hub`, a caller INSIDE the broken package) that cannot be freshly discovered by `callEdges` under any implementation** — `zz.Near` itself has no SSA body once `zz` is a types-only stub (no syntax), so there is no SSA function to resolve as a caller. That edge is recoverable ONLY via carry-forward (Phase 4), not via the `callEdges` rewrite. Fixed the test to assert only the clean-caller case (`testmod.main -> zz.Hub`), matching spec's literal scenario ("a broken package that is called by several clean packages"), and added a comment cross-referencing where the same-package case is actually proven (`TestBuildIndex_CarryForwardRecoversBrokenPackageInternalEdge`, 4.3).

2. **3.9/3.10 could not be given an organic end-to-end RED/GREEN cycle.** `callEdges`' `packages.Visit`-based construction makes the import closure complete BY CONSTRUCTION for every package it actually creates (design.md's own words) — the only way `create()` skips a reachable package is `p.Types == nil`, and `assertImportClosure`'s walk treats that case as "not our problem" (matching design), not a violation. Empirically verified (scratch probe against a genuinely unresolvable import) that `go/packages` still produces a non-nil, walkable `p.Types` placeholder even for a package that can't be resolved — so there is no observed environment condition under which the real pipeline reaches a closure violation, consistent with why the spec restricts closure-failure testing to a direct hand-built call (3.1/3.2) and explicitly forbids ever building an incomplete `ssa.Program` and calling `Build()` on it, even in a subprocess. Implemented 3.9/3.10 as a downstream-parity test instead: it injects the SAME Manager-level bookkeeping (`lastBuildErrors`/`failedStamps`) that `buildAsync`'s real failure branch would write for EITHER cause, using the same test-only direct-map-manipulation pattern already established in this file (`TestSupersededBuild_ClosesDone`, `TestBuildAsync_ForeignStateWithSameStampIsIgnored`), and confirms the served response (`Freshness.Stale`, `IndexError`, symbol resolution, byte-identical graph.db) is structurally indistinguishable from 3.7/3.8's. The actual wiring (3.10) required zero new code: `callEdges`'s error and the >50% cap's error already return through the exact same unconditional `buildIndex` → `return err` path.

3. **Pre-existing `build_test.go` tests broke** once partition landed, because they used a SINGLE broken package (a syntax error in `main`) as their failure trigger — under partial-build degradation that is no longer a whole-build failure (1-of-2 packages = 50%, not >50%). Added `breakMajority`/`fixMajority` helpers (both packages broken/fixed) and repointed 5 legacy tests (`TestBuildAsync_FailureServesStaleAndRecordsError`, `TestBuildAsync_SuccessClearsError`, `TestEnsureFresh_NoRebuildLoopOnPersistentFailure`, `TestEnsureFresh_ColdFailureIsNotRelaunched`, `TestEnsureFresh_AbandonedColdFailureIsRecorded`) at their existing `writeFile("broken.go", ...)` call sites — no assertions changed, only the fixture now triggers genuine (majority-broken) failure instead of a single-package error that no longer aborts the build. The `>50%` cap's error message deliberately keeps the pre-existing `"repo does not type-check"` prefix so `strings.Contains(err.Error(), "type-check")` assertions in these tests needed no changes.

### Gates (all run from repo root, real output)

- `go build ./cmd/droids-mem` → exit 0, no output
- `go test -count=1 ./...` → all packages `ok` (cmd/droids-mem, internal/db,
  internal/graph, internal/mcpserver, internal/scrub, internal/share,
  internal/state, internal/store, internal/tui)
- `go vet ./...` → exit 0, no output
- `golangci-lint run --timeout 5m` → `0 issues.` (one round-trip: `nolintlint`
  flagged 5 unused `//nolint:nilerr` directives in `carry.go` — `nilerr` never
  actually fired there since the returns are literal `(nil, nil)`, not the
  captured `err` variable; removed the stale directives, replaced with plain
  comments, re-ran clean)

### Line split (git diff --numstat / wc -l on new files)

Production:
- `internal/graph/index.go`: +159/-14 (partition/pkgSet, `assertImportClosure`, `callEdges` rewrite + `edgeSet` type, `>50%` cap, carry-forward wiring)
- `internal/graph/carry.go` (new): 93 lines

Tests:
- `internal/graph/build_test.go`: +142/-9 (`breakMajority`/`fixMajority` helpers + 5 repointed fixtures + 2 new tests: majority-broken cap, closure-parity)
- `internal/graph/index_test.go`: +226/-0 (closure direct-call ×2, broken-package-symbols, broken-package-in-edges, carry-forward end-to-end)
- `internal/graph/carry_test.go` (new): 146 lines

Total production: ~252 added / 14 removed. Total test: ~514 added / 9 removed.
Grand total ~766 added / 23 removed — **notably over both PR2's own ~405-line
forecast and the tasks.md-approved ~400-430 `size:exception` ceiling** (see
Risks below; flagged for the orchestrator/maintainer, not silently absorbed).

### Risks / deviations

- **Line count exceeds the approved size exception.** tasks.md pre-approved
  PR2 as a "soft `size:exception`" up to ~400-430 lines / ~60-75 min review.
  Actual is ~766 added lines (production ~252, tests ~514) — roughly 1.8x the
  approved ceiling. The overage is concentrated in tests: 4 integration tests
  needed real `copyFixture`+`writeFile` broken-tree fixtures (no lighter
  harness existed), `carry_test.go` needed a from-scratch hand-seeded-db
  helper (`seedPrevGraph`), and 5 pre-existing tests needed their break
  fixture upgraded from single-package to majority-broken (`breakMajority`/
  `fixMajority`), which the original forecast likely did not anticipate as a
  cost of the partition rewrite. No test was cut to fit the budget — all are
  load-bearing per the spec's own scenario list. Flagging for maintainer
  re-forecast/re-approval rather than silently exceeding the agreed boundary.
- No droids-mem MCP tools available to this sub-agent session — only Engram
  was used. Orchestrator is expected to mirror into droids-mem per the
  dual-persist convention; flagging the gap per instructions, not treating it
  as a failure.
- `set.kept` (the deduped symbol-emission list) includes BOTH broken and
  clean packages by design — "kept" means "not discarded", not "clean". This
  reading was inferred from cross-referencing design.md's Requirement text
  ("for every kept package, broken or clean") against its `pkgSet{kept,
  broken}` struct comment ("kept = deduped variants"); flagging in case the
  intended reading was `kept == clean-only`, which would have made the
  spec's "Symbols are fresh from AST for every package" requirement
  unsatisfiable by construction.
- `carried_units` (task 4.3's phrase) is NOT threaded to any field/variable
  visible outside `buildIndex` in this PR — see the inline task-4.3 note in
  tasks.md. PR3's 5.1-5.3 will need to either recompute it or have `carry.go`
  return it explicitly; currently only `brokenPkgNames` (buildIndex-local)
  carries that information forward within a single build.
- Untouched by design and verified still present/unmodified from PR1:
  `edges.dispatch` schema column (does not exist), `Freshness.StaleUnits`
  (does not exist), the `carried` flag, tasks 6.3-6.10, `query.go`/`render.go`
  (no changes).
- `openspec/` remains untracked in git per repo convention; nothing staged or
  committed. No branch/commit/push operations performed — orchestrator owns
  git.

---

# PR1 (prior entry, unmodified below)

# Apply progress: graph-partial-index — PR1

Scope: Phase 1 (all), Phase 2 (all), Phase 6 tasks 6.1-6.2 only. Nothing from
PR2/PR3 touched (`DeleteSyntheticNodes` call, closure precondition, `carry.go`,
`edges.dispatch`, `Freshness.StaleUnits`, tasks 6.3-6.10 all untouched).

Branch: `feat/graph-partial-index-pr1` (verified via `git branch --show-current`
before any edit).

## Tasks completed

- [x] 1.1 RED `index_test.go` — dedupe: no duplicate qname, findSymbol unambiguous
- [x] 1.2 GREEN `index.go` — `Tests: true` + `dedupeVariants(pkgs)`
- [x] 1.3 RED `stamp_test.go` — `_test.go` edit must move `stamp()`
- [x] 1.4 GREEN `stamp.go` — dropped `_test.go` exclusion at the old `:97`, comment updated
- [x] 2.1 `testdata/edges_clean.golden` captured (post Phase 1, `DeleteSyntheticNodes` still present)
- [x] 2.2 `build_test.go`: `TestCallEdges_MatchesCleanGoldenEdgeSet` — passes now (characterization test), will re-verify unchanged in PR2 after `DeleteSyntheticNodes` removal
- [x] 6.1 RED `query_test.go` — production callers must precede test callers under a cap
- [x] 6.2 GREEN `query.go` — `neighborLevel` ORDER BY → `is_test, (s.package != ?), s.qname`, escaped LIKE

## RED failure messages (actual)

1.3 (`TestStamp_TestFileEditMovesStamp`, before `stamp.go` fix):
```
stamp_test.go:35: test-file edit did not move stamp: still "ve92b01f9:2:41:1786745399426213404"; with Tests:true a _test.go edit must invalidate the cache
```

1.1 (`TestDedupeVariants_NoDuplicateQNameWithTestsEnabled` + `_FindSymbolResolvesUnambiguously`,
after `Tests: true` added but before `dedupeVariants`):
```
index_test.go:52: 2 duplicate qname group(s) in symbols table, want 0
index_test.go:60: zz.Hub appears 2 time(s) in symbols, want exactly 1 (findSymbol would report it ambiguous)
index_test.go:86: zz.Hub did not resolve to a single symbol, got matches=[{QName:zz.Hub ...} {QName:zz.Hub ...}] hint="multiple symbols share that name; re-query with one of the qnames in matches"
```

6.1 (`TestNeighborOrdering_ProductionBeforeTestUnderCap` + `_ProximityPreservedWithinTestGroup`,
before the ORDER BY fix):
```
query_test.go:44: test caller "zz.TestHubA" shown under the cap while a production caller exists: [...]
query_test.go:83: production caller "testmod.main" (index 2) sorted after a test caller
```

Note on 1.1/1.2 TDD sequencing: the duplication bug only manifests once
`Tests: true` is set (packages.Load doesn't emit variants otherwise), so the
RED test genuinely could not fail before that one-line config flip existed.
Sequence used: (a) add `Tests: true` alone, (b) write the RED test, confirm it
fails for the stated reason (duplicate rows / ambiguous match — verified
above), (c) add `dedupeVariants` and wire it in → GREEN. This matches the task
doc's own framing ("these three land in one commit").

Note on 2.2: intentionally a characterization test, not a bug-catching RED —
the task doc states explicitly it "passes now"; it exists to protect PR2's
`DeleteSyntheticNodes` removal, not to catch a PR1 bug.

## Lockstep fix required but not separately itemized

`graph_test.go`'s pre-existing `TestStampIgnoresTestFiles` asserted the exact
opposite of task 1.3's new invariant (test-file edits must NOT move the
stamp). Left unchanged it would fail post-1.4. Renamed to
`TestStampMovesOnSourceEdit`, narrowed to the source-edit half only (the
test-file half now lives in `stamp_test.go`'s new test) — done in the same
commit as 1.4's GREEN, not a separately hidden RED cycle.

## Gates (all run from repo root, real output)

- `go build ./cmd/droids-mem` → exit 0, no output
- `go test -count=1 ./...` → all packages `ok` (cmd/droids-mem, internal/db,
  internal/graph, internal/mcpserver, internal/scrub, internal/share,
  internal/state, internal/store, internal/tui)
- `go vet ./...` → exit 0, no output
- `golangci-lint run --timeout 5m` → `0 issues.`

## Line split (git diff --numstat)

Production:
- `internal/graph/index.go` +41/-1 (`Tests: true`, `dedupeVariants`, wired into symbol loop)
- `internal/graph/query.go` +10/-4 (`neighborLevel` ORDER BY)
- `internal/graph/stamp.go` +11/-9 (drop `_test.go` exclusion, comment)

Tests:
- `internal/graph/build_test.go` +70/-0 (golden-parity test + imports)
- `internal/graph/graph_test.go` +5/-9 (renamed/narrowed `TestStampIgnoresTestFiles`)
- `internal/graph/index_test.go` +91 (new file, dedupe RED/GREEN tests)
- `internal/graph/query_test.go` +89 (new file, ordering RED/GREEN tests)
- `internal/graph/stamp_test.go` +37 (new file, stamp lockstep RED/GREEN test)
- `internal/graph/testdata/edges_clean.golden` +5 (fixture data, not code)

Total production ~62 added / 14 removed; total test ~292 added / 9 removed
(plus a 5-line golden fixture) — within PR1's forecast "Low" 400-line risk
bucket.

## Risks / deviations

- No droids-mem MCP tools available to this sub-agent session; only Engram was
  used for the mirrored save. Orchestrator is expected to mirror into
  droids-mem per the dual-persist convention — flagging the gap per
  instructions, not treating it as a failure.
- `dedupeVariants` iterates `pkgs` in `packages.Load`'s returned order and
  keeps first-seen-per-`PkgPath` unless a later `" ["`-variant supersedes it;
  this matches the design's literal spec ("keep the in-package test variant
  when present else the plain one") and was verified end-to-end against the
  real `go/packages` loader (not mocked), so the real ID-shape assumption
  (`" ["` substring) is confirmed correct on this toolchain.
- Untouched by design and verified still present/unmodified: `cg.DeleteSyntheticNodes()`
  call in `callEdges` (index.go), the closure precondition, `carry.go` (does not
  exist yet), `edges.dispatch` column, `Freshness.StaleUnits`, tasks 6.3-6.10.
- `openspec/` remains untracked in git per repo convention; nothing staged or committed.
