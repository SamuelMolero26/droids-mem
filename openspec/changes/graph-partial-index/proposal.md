# Proposal: Partial Go Graph Index — Per-Package Degradation, Test Coverage, Dispatch Labels

Formalizes ADR-0034 inventory item 4, decision 7a, and decision 2 (amended) —
the settled scope of the first SDD slice. Every decision below is already made
and measured in the ADR; this proposal does not re-litigate them.

## Intent

Two contract violations ship today, both measured on this repo:

1. **One broken file discards the whole fresh graph.** `index.go:63-67` hard-returns
   on the *first* package error, before `writeGraphDB`, throwing away the
   per-package results `go/packages` already produced. A mid-edit tree — the
   overwhelmingly common state when an agent queries — serves the last good
   whole graph. Body-local errors stay confined to one package (dependents
   type-check against still-well-formed exported signatures), so discarding
   everything is a ~9-package overreaction to a 1-package fault.
2. **The graph misses callers that can fire.** ADR-0020's contract sentence is
   "may report a caller that can't fire, never misses one that can". `_test.go`
   is excluded from the semantic tier: **0 of 675 indexed symbols** come from a
   test file, and `store.Store.Save` reports **5 callers while 84 distinct test
   functions call it**. The blind spot is largest exactly on exported API, where
   blast-radius questions get asked.

Naively relaxing the gate is not the fix: at *edge* granularity an
exported-surface break serves **29.0% of edges** (the break lands in the package
owning 68% of outbound edges). Partial indexing must ship with edge
carry-forward, which lands the same scenario at **~97%** and body-local at
**~100%**. This slice is what makes the graph an actual reachability tool.

## Scope

### In Scope

**A. `buildIndex` restructure (inventory 4, decisions 8 + 8a)**

- Symbols fresh from AST for **every** package, broken included —
  `appendDeclSymbols` uses zero type information, so `p.Syntax` + `p.PkgPath`
  survive a type error intact. **Symbols are never carried.**
- SSA via manual `prog.CreatePackage(p.Types, nil, nil, true)` (types-only stub)
  for broken packages. **Never `ssautil.AllPackages`** — it filters on the
  *transitive* `p.IllTyped` and collapsed the graph to 19.9% of edges even when
  handed a pre-filtered clean subset.
- The reachable-import-closure precondition is **computed and asserted BEFORE
  `prog.Build`**, never an error path. On failure: serve the previous whole
  graph (same path as the >50% cap).
- `cg.DeleteSyntheticNodes()` **removed unconditionally**, plus a regression test
  pinning clean-tree edge-set equality (measured identical; 158 in-edges into the
  broken package survive with it removed vs 39 with it kept).
- **Edge-only carry-forward**: only edges whose *caller* is inside a broken
  package, spliced from the previous `graph.db`, endpoints remapped old-id→new-id
  by qname, dropped on a qname miss. Cross-unit edges (clean caller → broken
  callee) are **not** carried.
- **>50%-broken safety cap**: serve the whole previous graph.

**B. Test symbols in the semantic tier (decision 7a)**

- `Tests: true` in the semantic tier's `packages.Config` — same config struct,
  same slice as A.
- Caller counts **split** in responses (`callers: 89, 84 in tests`); test symbols
  are identifiable by their `_test.go` file path.

**C. `dispatch` label (decision 2, amended)**

- New `dispatch` column on `edges` (`static` / `interface`) from
  `callgraph.Edge.Site.Common().IsInvoke()`, **nil-guarded** (synthetic edges
  have no `Site`).
- Surfaced **response-level** as a split count plus a hint when interface
  dispatch dominates. **NOT per-row on the wire** — per-edge costs 49× the bytes
  for identical information at the 50-cap.

**D. Carry-forward honesty labels (decision 8, amended)**

- `carried_units` list in `graph.db` `meta`.
- `Freshness` gains `stale_units`, **capped**: first 5 + total + hint
  (`stale_units[5 of 213]{...}` + "run graph_build_wait for the full list").
- Per-symbol `carried` flag derived by checking the queried symbol's package
  against that set.
- `stale: true` **narrows to genuine build failures** (I/O, corrupt cache).

### Out of Scope — later slices

- The tree-sitter mapper, the `gotreesitter` dependency, and the build-tag matrix
  (inventory 1, 5).
- The `precision` column and the `imports` table — mapper prep only (inventory 2).
- Widening `indexedExtensions()` beyond `.go` (inventory 3).
- The `--callers-only` / body-suppressing response mode (inventory 8).
- `CONTEXT.md` / `CLAUDE.md` glossary edits — deferred until this lands.

## Capabilities

### New Capabilities

- `code-graph-partial-index`: per-package build degradation, edge carry-forward,
  the >50% safety cap, and per-unit freshness labeling (`stale_units`, `carried`).
- `code-graph-caller-fidelity`: test symbols indexed in the semantic tier, and
  the response-level test/production and static/interface caller splits.

### Modified Capabilities

- None — no `openspec/specs/` exists yet.

## Approach

Everything happens in memory inside **one** `buildIndex` pass before the single
wholesale write; `graph.db` is still swapped by atomic rename and never updated
in place. Ordering inside the pass:

1. Load with `Tests: true`; partition packages by `len(p.Errors) > 0`.
2. If broken > 50% → serve previous whole graph, done.
3. Symbols from AST for all packages; assign ids.
4. Compute the reachable-import closure over the packages to be created; assert
   it. Fail → serve previous whole graph.
5. `CreatePackage` per package (full for clean, types-only stub for broken) →
   `prog.Build()` → `cha.CallGraph` **without** `DeleteSyntheticNodes`.
6. Collect edges with `dispatch`; splice carried edges for broken-package callers
   from the previous `graph.db`, remapped by qname.
7. Write, recording `carried_units` in `meta`.

Rationale for each choice is in ADR-0034 decisions 8, 8a, 7a and 2 (amended) —
not restated here.

## Affected Areas

| Area | Impact | Description |
|---|---|---|
| `internal/graph/index.go` | Modified | Gate at `:63-67` removed; `Tests: true`; symbol pass over all packages; `callEdges` rebuilt around manual `CreatePackage` + closure precondition + `dispatch`; `writeGraphDB` writes the new column and `carried_units` |
| `internal/graph/graph.go` | Modified | `schema` const gains `edges.dispatch`; `Freshness` gains capped `stale_units` |
| `internal/graph/carry.go` | New | Read previous `graph.db`, qname old-id→new-id remap, caller-in-broken-package filter |
| `internal/graph/query.go`, `render.go` | Modified | Test/production and static/interface caller splits, `carried` flag, `stale_units` render + hint |
| `internal/graph/*_test.go` | New/Modified | Clean-tree edge-set-equality regression (no-`DeleteSyntheticNodes`), broken-package fixtures, carry-forward, >50% cap, closure-precondition failure path, dispatch split, test-caller split |
| `docs/adr/0034-*.md` | Modified | Mark inventory 4 / 7a / dispatch as landed (gitignored — zero review lines) |

## Measured Stakes

| Signal | Today | After this slice |
|---|---|---|
| Edges served, exported-surface break | 29.0% (naive relaxation) | ~97% |
| Edges served, body-local break | 87.7% fresh | ~100% |
| Symbols from `_test.go` | 0 of 675 | indexed |
| `store.Store.Save` reported callers | 5 | 89 (84 in tests) |
| `store.ValidationError.Error` callers | 40, unlabelled (≥26 cannot fire) | 40, split by dispatch |

## Risks

| Risk | Likelihood | Mitigation |
|---|---|---|
| **The SSA panic is unrecoverable.** `prog.Build` panics `"unsatisfied import: Program.CreatePackage(...) was not called"` from goroutines it spawns itself, so `defer recover()` in the caller never sees it. Under stdio this kills the whole MCP server on a mid-edit tree — strictly worse than serve-stale | Med | The closure check is a **precondition asserted before `Build`**, never an error path. **No test may assert the panic** — it kills the test binary. Test the precondition's *rejection*, not the panic |
| ~3× graph size from `Tests: true` (packages 9→31, decls +208%) | High (certain) | Accepted trade, measured: load time at parity (~1–1.7 s warm either way; build cache dominates). Paid in size, not speed |
| Schema change (`dispatch` column + `carried_units` meta) auto-invalidates every cached graph once | High (certain) | Expected, one-time, by design — `currentGen` hashes the schema (`stamp.go:47`). This is why 7a and `dispatch` ship *with* the restructure: splitting them re-invalidates twice for nothing |
| Carried edges resurrect a call that was deleted | Low | Only caller-in-broken-package edges are carried; qname miss drops the endpoint; cross-unit edges are never carried |
| `Site` is nil on synthetic edges → nil-deref in the dispatch label | Med | Nil-guard at collection; covered by the clean-tree regression test |
| Removing `DeleteSyntheticNodes` admits spurious edges | Low | Measured zero spurious edges in any scenario; the edge-set-equality regression test pins clean-tree parity |

## Rollback Plan

The whole slice is internal to `internal/graph`. `graph.db` is **rebuilt
wholesale, never migrated** — there is no persisted-format migration ladder to
unwind and no user data at risk (graph rows are derived from source).

**Rollback = revert the commit.** Cached graphs rebuild once more on the old
schema hash the next time they are queried. `mem.db` is untouched. No CLI, MCP
tool name, or response field that predates this slice is removed.

## Delivery Forecast

`Chained PRs recommended: No` · `800-line budget risk: Medium-High`

| Component | Forecast (add+del) |
|---|---|
| `index.go` restructure + `dispatch` collection | ~200 |
| `carry.go` splice | ~130 |
| Schema + `Freshness.stale_units` + render/query splits | ~120 |
| Tests (TDD-first, the bulk) | ~300 |
| **Total** | **~750** |

Under the pre-selected force-chained 800-line budget, but with little headroom.
**Chaining is not proposed**: the ADR pins these three items to one slice
precisely because they share the build pass and schema generation, and no half is
independently correct — carry-forward without the gate relaxation is dead code,
the gate relaxation without carry-forward ships the 29.0% regression, and the
`dispatch` column alone would re-invalidate every cached graph for no user-visible
gain. Splitting purely to chase a budget number is exactly what the standing rule
forbids.

*Contingency only if the diff genuinely overruns 800:* the sole defensible
boundary is the **render/query surface** (dispatch split counts, `stale_units`
render, `carried` flag) as a child PR — the schema column and the build pass must
still land together in the parent.

Strict TDD is active for this project: apply will be test-first (RED → GREEN →
refactor), with the clean-tree edge-set-equality regression written before
`DeleteSyntheticNodes` is removed.

## Dependencies

- No new Go modules. `golang.org/x/tools` stays at its pinned version (the
  measurements were taken on v0.48.0).
- No prerequisite work: the schema-derived stamp generation, the `.git` repo
  anchor, and the build-output walk exclusion all landed in PR #100.

## Success Criteria

- [ ] A repo with one type-erroring package serves fresh symbols for **every**
      package, including the broken one.
- [ ] Edges whose caller is in the broken package are carried from the previous
      graph, qname-remapped; a deleted symbol's edges are dropped.
- [ ] Cross-unit edges (clean caller → broken callee) are **not** carried.
- [ ] On a clean tree, the edge set with `DeleteSyntheticNodes` removed is
      **identical** to the current build's — pinned by a regression test.
- [ ] >50% broken packages → the whole previous graph is served, unchanged.
- [ ] A failed import-closure precondition serves the previous whole graph and
      **never panics**; the MCP server survives.
- [ ] `store.Store.Save` reports its test callers, split
      (`callers: N, M in tests`).
- [ ] A CHA fan-out is distinguishable from a real hub: `callers: 40, 36 via
      interface dispatch`, with a hint when dispatch dominates.
- [ ] `stale_units` is capped at 5 + total + hint; `stale: true` appears only on
      genuine build failures (I/O, corrupt cache).
- [ ] `go build ./cmd/droids-mem`, `go test -count=1 ./...`, `go vet ./...`,
      `golangci-lint run --timeout 5m` all clean.

## Proposal question round

Interactive mode is on, but this executor cannot prompt directly. Five product
questions; the assumption taken is stated — correct any before `sdd-spec`.

1. **Test callers in the default response.** Assumed: test callers are indexed
   and shown **split but included** in the headline count (`callers: 89, 84 in
   tests`). The alternative is a production-first headline (`callers: 5 (+84 in
   tests)`). Which reads better to an agent asking a blast-radius question?
2. **The ~3× graph size.** Assumed acceptable (size, not speed). Is there a disk
   ceiling per repo that should trigger a warning in `doctor`, or is it genuinely
   unbounded for now?
3. **Carried-unit visibility.** Assumed: a per-symbol `carried` flag plus a capped
   `stale_units` list. Should a *fully* carried response (every neighbor carried)
   also carry a stronger warning, or is the flag enough?
4. **Precondition-failure honesty.** Assumed: a failed import closure is silently
   equivalent to the >50% cap — previous whole graph, `stale: true`. Should the
   response distinguish "too broken to splice" from "splice machinery could not
   run", or is one degraded state enough for an agent?
5. **Interface-dispatch hint threshold.** Assumed: a hint fires when
   interface-dispatch edges *dominate*. What is "dominate" — >50% of callers, or
   any non-zero interface count? The `.Error()`/`String()`/`Close()` shape is the
   case that motivated it.
