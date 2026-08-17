# Design: Partial Go Graph Index — Per-Package Degradation, Test Coverage, Dispatch Labels

Implements ADR-0034 decisions 8a, 7a, 2 (amended 2026-08-14), contract item 4,
inventory item 4. Those decisions are settled and measured; this document maps
them onto `internal/graph` and records where the ADR's stated mechanism needed
correcting against the code.

## Technical Approach

One `buildIndex` pass, one wholesale write, atomic rename — unchanged. The
all-or-nothing gate (`index.go:63-67`) becomes a partition; SSA creation moves
from `ssautil.AllPackages` to an explicit `packages.Visit` + `prog.CreatePackage`
walk with a closure assertion before `prog.Build`; carry-forward reads the
previous `graph.db` once, read-only, outside the Manager handle cache. No
migration ladder (graph.db is rebuilt, never migrated).

## Architecture Decisions

### Decision: package-variant dedupe is a precondition of `Tests: true`

**Choice**: after `Load`, pick exactly ONE variant per `PkgPath` for symbol rows
— the test variant (`p.ID` contains `" ["`) when present, else the plain one —
and skip the synthesized `<pkg>.test` main. SSA is still created for *every*
loaded package and dependency, both variants included.
**Alternatives**: emit rows for every returned package (the ADR's literal "symbols
from AST for every package").
**Rationale**: **this is where the ADR does not survive contact with the code.**
`Tests: true` makes `packages.Load` return up to four variants per tested
package, and the in-package test variant's `p.Syntax` contains the SAME
production files as the plain variant, with the SAME `PkgPath`. Iterating all of
them duplicates every production symbol row (duplicate `qname`, `symbols.qname`
has no UNIQUE constraint so the write silently succeeds), makes `findSymbol`
return "ambiguous" for every symbol in a tested package, and makes `byPos`
last-write-wins. The measured "decls +208%" is consistent with that duplication.
Dedupe is required for correctness, not tidiness. Because both variants' SSA
functions sit at identical `file:line`, `byPos` maps them onto the one retained
row and their edges collapse in the `edges` map — no edge is lost by deduping.

### Decision: SSA domain = `packages.Visit` closure; the assert is a real check

**Choice**: `packages.Visit(pkgs, nil, create)`; `create` calls
`prog.CreatePackage(p.Types, p.Syntax, p.TypesInfo, true)` for clean packages and
`prog.CreatePackage(p.Types, nil, nil, true)` for `len(p.Errors) > 0`, skipping
`p.Types == nil`. Then `assertImportClosure(prog, visited)` walks every created
package's `p.Imports` and fails if any importee has no `ssa.Package`. Only then
`prog.Build()`; `cha.CallGraph(prog)`; **no** `DeleteSyntheticNodes`.
**Alternatives**: `ssautil.AllPackages` (rejected in 8a — transitive `IllTyped`
filter, 19.9% of edges); omitting broken packages (rejected — unrecoverable
panic from `Build`'s own goroutines).
**Rationale**: `Visit` makes the closure complete *by construction*, so the
assert is cheap; it is not vacuous, because a package with `p.Types == nil`
(unresolvable dep) is skipped and would otherwise panic. Failure is a normal
`error` return — never a panic path.

### Decision: previous graph opened once, read-only, bypassing `connEntry`

**Choice**: `carry.go` opens `dbPath` with a private
`sql.Open("sqlite", "file:"+dbPath+"?mode=ro")` — the same pattern
`buildlock.go:117` (`stampOnDisk`) already uses — once per `buildIndex`,
`defer db.Close()` at function scope, only when `len(broken) > 0`.
**Alternatives**: reuse `Manager.open`/`connEntry`; open per broken package.
**Rationale**: `buildIndex` has no `*Manager`, and threading one in would couple
the build to the refcounted retire/release lifecycle for a read that finishes
before `writeGraphDB` even starts. A separate read-only handle is invisible to
`conns`; concurrent readers on the live handle are unaffected (read/read), and
the rename happens after our handle is closed.
**No previous graph** (first-ever build on a broken tree): zero carried edges,
build proceeds — symbols are still fresh for every package, the broken package
simply has no out-edges. It is still listed in `carried_units` (semantics: *this
unit's edges are not freshly analyzed*), so the response stays honest.
**Carry-forward is strictly best-effort**: any open/query/scan error yields zero
carried edges and the build continues. This is load-bearing, not defensive —
the previous `graph.db` predates the `dispatch` column, so `SELECT e.dispatch`
against it fails with `no such column` on exactly the first post-upgrade build.

### Decision: degraded outcomes reuse the existing failure path

**Choice**: `>50%` broken and closure-assert failure both `return` a distinct
error from `buildIndex`, writing nothing.
**Rationale**: `buildAsync` already records `lastBuildErrors` + `failedStamps`,
so the old graph.db stays in place, is served with `Stale: true` +
`IndexError`, and is not doomed-rebuilt on every query. "Serve the previous whole
graph" needs zero new machinery. It also answers proposal question 4 for free:
the two states are distinguishable by `IndexError` text at no cost.

### Decision: no per-row `origin`/`dispatch` on the wire, no new symbol column

**Choice**: test-ness is derived in SQL, `s.file LIKE '%\_test.go' ESCAPE '\'`
(the escape matters — bare `_` is a LIKE wildcard and would misclassify
`helpertest.go`). Splits are response-level scalars.
**Rationale**: decision 2's measured 49× byte waste for `precision` applies
unchanged; `loc` already carries the `_test.go` path per row.

## Data Flow

    packages.Load(Tests:true) ──► partition by len(p.Errors)
         │                              │
         │                        >50%? ─► return err ─► old graph served stale
         ▼
    dedupeVariants ──► appendDeclSymbols (ALL kept pkgs) ──► ids = int64(i+1)
         │                                                        │
         ▼                                                        ▼
    packages.Visit ─► CreatePackage(clean|stub) ─► assertImportClosure ─┬─ err ─► old graph
         │                                                             │
         ▼                                                             ▼
    prog.Build ─► cha.CallGraph (no DeleteSyntheticNodes) ─► edges{pair→dispatch}
         │                                                        ▲
    prev graph.db (ro, once) ─► carried edges, qname-remapped ────┘
         │
         ▼
    writeGraphDB(… , carriedUnits) ─► tmp ─► chmod 0600 ─► atomic rename

## File Changes

| File | Action | Description |
|---|---|---|
| `internal/graph/index.go` | Modify | Gate → partition; `Tests: true`; `dedupeVariants`; symbol pass over kept packages; `callEdges` rebuilt (Visit + CreatePackage + assert + `dispatch`, no `DeleteSyntheticNodes`); `writeGraphDB` writes `dispatch` and `meta.carried_units` |
| `internal/graph/carry.go` | Create | `openPrevGraph`, `carriedEdges(prev, brokenPkgs, qnameToID)` — caller-in-broken-package filter, old-id→new-id remap by qname, drop on miss, best-effort |
| `internal/graph/graph.go` | Modify | `schema`: `edges.dispatch TEXT NOT NULL DEFAULT 'static'`; `Freshness` gains `StaleUnits []string` + `StaleUnitsTotal int`; `open()` also reads `meta.carried_units` |
| `internal/graph/stamp.go` | Modify | **Drop the `_test.go` exclusion at `:97`** (see below). `indexedExtensions()` unchanged (`.go`) |
| `internal/graph/query.go` | Modify | Caller split queries, `Carried`, ordering, hints |
| `internal/graph/render.go` | Modify | Render splits, `stale_units[N of M]`, `carried`; reword `staleGraphHint`/`writeFreshness` ("repo no longer type-checks" is now false for a partial build) |
| `internal/graph/*_test.go`, `testdata/` | Create/Modify | See Testing Strategy |

### Stamp census — REQUIRED lockstep change

`stamp()` excludes `_test.go` (`stamp.go:97`), and its own comment says the
filter must drop "in lockstep" if test indexing is enabled. With `Tests: true`
it must: otherwise editing a test file never moves the stamp and the graph never
picks up a new test caller. This is a correctness fix, not an optimization.

Note the ADR/proposal claim that `carried_units` "flows through the
schema-derived stamp generation" is **inaccurate**: `carried_units` is a `meta`
row, not DDL, so it does not change `stampGen(schema, exts)`. The one-time
invalidation is delivered by `edges.dispatch` alone — which is sufficient, and
is a further reason the two ship together. Widening the file census also changes
`count`/`size`, giving a second, independent invalidation.

## Interfaces / Contracts

```go
// index.go
type pkgSet struct{ kept, broken []*packages.Package } // kept = deduped variants
func assertImportClosure(prog *ssa.Program, created []*packages.Package) error
// edges carry their dispatch label; static wins a static/interface collision on
// the same pair (the stronger reachability claim).
type edgeSet map[[2]int64]string // "static" | "interface"

// carry.go — best-effort; any error ⇒ (nil, nil)
func carriedEdges(dbPath string, broken map[string]bool, byQName map[string]int64) (edgeSet, error)

// graph.go
type Freshness struct {
    …
    StaleUnits      []string `json:"stale_units,omitempty"`       // first 5
    StaleUnitsTotal int      `json:"stale_units_total,omitempty"`
}

// query.go — all depth=1 only, computed in SQL, independent of maxNeighbors
type SymbolResponse struct {
    …
    CallersInTests      int  `json:"callers_in_tests,omitempty"`
    CallerTestFiles     int  `json:"caller_test_files,omitempty"`  // distinct _test.go files
    CallersViaInterface int  `json:"callers_via_interface,omitempty"`
    Carried             bool `json:"carried,omitempty"`            // queried symbol's pkg ∈ carried_units
}
```

`dispatch` source: `e.Site != nil && e.Site.Common().IsInvoke()` → `"interface"`,
else `"static"` (nil `Site` = synthetic edge ⇒ `"static"`).

### Ordering and the cap

`neighborLevel` (`query.go:483-486`) becomes
`ORDER BY is_test, (s.package != ?), s.qname` — production before test, then
closest-first within each group, then alphabetical. The `(s.package != ?)`
proximity clause (issue #49) is **kept**, not replaced: `is_test` is prepended
above it, so the cap still keeps the closest neighbors, now within a guaranteed
production-first partition.

The production guarantee is load-bearing, verified on this repo: `internal/store`
has 4 in-package test files (`package store` — same package path, so they sort
*first* under today's clause) and 21 external ones, while `Store.Save`'s
production callers live in `internal/mcpserver` and `cmd/droids-mem` (different
packages, sorted after same-package rows, then only by alphabetical qname). Any
production-first behavior today is accidental alphabetical luck and inverts on a
differently-named package. With 86 test callers against a 50 cap, that is exactly
how an agent sees zero production callers and concludes a signature change is
test-only. `is_test` comes from the file-path suffix — still no new column,
consistent with the no-`origin`-column decision.

**Cap = 50, unchanged**, and stays the single tunable `var maxNeighbors` at
`query.go:53`. Benchmarked under `Tests: true` on this repo: 516 symbols with
callers, caller-count p99 = 43, 99.6% show completely at 50, p95 response size is
flat from 50 through 200 (raising it buys ≤0.19pp for zero saving); the 2 symbols
that truncate at 50 are both `_test.go` fixture helpers. No code branches on the
value, and no second numeric constant is introduced. **Cap-independent**: the
ordering, all three split counts, `CallersTotal`/`CalleesTotal`, `Truncated`,
`truncatedHint`. **Cap-dependent**: only the length of the shown slice. The
dispatch hint fires at one named constant `dispatchHintRatio = 0.5` (proposal Q5
answered: >50% of the depth=1 caller total, not any non-zero count).

## Testing Strategy

Strict TDD: RED → GREEN → refactor. `go test -count=1 ./...`, `go vet ./...`,
`golangci-lint run --timeout 5m`.

**CRITICAL — no test may reach the SSA panic.** `prog.Build` panics from
goroutines it spawns; a `recover()` cannot catch it and the test binary dies. No
test constructs an incomplete program and calls `Build`. The precondition is
tested by calling `assertImportClosure` **directly** with a hand-built
created-set that omits a reachable import, asserting the error. That is the
whole coverage of the panic scenario.

Fixtures use the existing harness only: `build_test.go:copyFixture` copies
`testdata/testmod` into `t.TempDir()`; a `writeInto(t, dir, rel, src)` helper
injects a break (a file with a type error) or a `_test.go` caller. No new
machinery.

| Layer | What | Approach |
|---|---|---|
| Unit | Import-closure rejection | `assertImportClosure` direct call, missing importee ⇒ error, no `Build` |
| Unit | qname remap / drop-on-miss / cross-unit exclusion | `carriedEdges` against a hand-seeded prev db |
| Unit | Old-schema prev db (`no such column: dispatch`) ⇒ 0 carried, build succeeds | seeded pre-change schema |
| Integration | **Edge-set equality (pins `DeleteSyntheticNodes` removal)** | Golden `testdata/edges_clean.golden` of `caller_qname,callee_qname` sorted, from the clean fixture. Capture order is load-bearing: enable `Tests:true`+dedupe first, capture the golden **while `DeleteSyntheticNodes` is still present**, then remove it — the test must still pass unchanged |
| Integration | Broken package ⇒ fresh symbols for ALL packages, carried edges present, cross-unit absent | `copyFixture` + injected break, assert against graph.db |
| Integration | >50% broken and closure failure ⇒ build errors, `graph.db` byte-identical, `Freshness.Stale` + `IndexError` | compare stamp/mtime before and after |
| Integration | Test-caller split, distinct-file count, dispatch split + hint | fixture with a `_test.go` caller and an interface call |
| Integration | Variant dedupe: no duplicate `qname` rows; `findSymbol` still resolves uniquely | count `qname` groups > 1 ⇒ fail |
| Unit | Editing a `_test.go` moves `stamp()` | pre/post stamp comparison |

## Threat Matrix

N/A — no routing, VCS/PR automation, or executable-file classification boundary
is added. `go/packages` already spawns `go list`; `Tests: true` changes only what
that pre-existing subprocess is asked to load, not how it is invoked. `_test.go`
detection is a display/ordering predicate, not a trust decision.

## Migration / Rollout

No migration. `graph.db` is written wholesale and swapped by atomic rename; the
`edges.dispatch` DDL change moves `currentGen` (`stamp.go:47`) so every cached
graph rebuilds exactly once, and the widened file census moves the stamp again
independently. Rollback = revert the commit; graphs rebuild once on the old
generation. `mem.db` and every migration ladder are untouched.

## Open Questions

- [ ] Proposal Q1 (headline caller count shape) and Q3 (fully-carried warning)
      are unanswered; this design assumes split-but-included counts and the flag
      alone. Both are render-layer only and cheap to change.
- [ ] Golden-file brittleness: `testdata/edges_clean.golden` must be regenerated
      if `testdata/testmod` changes. Accepted — the diff is a legitimate review
      signal, and the alternative (a test-only `deleteSyntheticNodes` bool) keeps
      dead code alive forever.
