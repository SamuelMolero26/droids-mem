# Code Graph Partial Index Specification

## Purpose

Defines degradation behavior for the Go semantic graph builder
(`internal/graph`) when part of the tree fails to type-check. A broken
package MUST NOT discard results already computed for the rest of the tree,
and any state served instead of a fresh build MUST be labelled honestly.

## Requirements

### Requirement: Per-package build partitioning

The system MUST partition `packages.Load` results into clean and broken sets
by `len(p.Errors) > 0`, replacing the prior all-or-nothing gate that
discarded the whole build on the first package error.

#### Scenario: One broken package among many

- GIVEN a tree of 9 packages where exactly 1 has a body-local type error
- WHEN `buildIndex` runs
- THEN the build proceeds using 8 clean packages and 1 broken package
- AND the build does not abort before `writeGraphDB`

### Requirement: Symbols are fresh from AST for every package

The system MUST emit symbol rows from AST alone (`p.Syntax` + `p.PkgPath`)
for every kept package, broken or clean. Symbols MUST NOT be carried forward
from a previous build under any circumstance.

#### Scenario: Broken package still yields symbols

- GIVEN a package with a type error in one function body
- WHEN `buildIndex` runs
- THEN symbol rows for every declaration in that package appear in the fresh
  `graph.db`, sourced from the current AST, not from a previous build

### Requirement: Reachable-import-closure precondition before SSA build

The system MUST compute and assert, before calling `prog.Build()`, that
every package reachable from a created package has a corresponding
`ssa.Package`. This assertion MUST be a precondition check with a normal
error return, never an error path discovered by a panic.

#### Scenario: Closure holds

- GIVEN a created-package set where every import of every created package is
  itself created
- WHEN the closure precondition is evaluated
- THEN it returns no error and the build proceeds to `prog.Build()`

#### Scenario: Closure violated

- GIVEN a hand-built created-package set that omits a package reachable via
  import from an included package
- WHEN the closure precondition is evaluated directly (without calling
  `prog.Build()`)
- THEN it returns a non-nil error
- AND the build serves the previous whole graph instead of proceeding to
  `prog.Build()`

### Requirement: Broken packages get types-only SSA stubs, not omission

The system MUST create an `ssa.Package` for every broken package using
`prog.CreatePackage(p.Types, nil, nil, true)` (a types-only stub). The
system MUST NOT omit broken packages from SSA package creation.

#### Scenario: Broken package participates in SSA without a syntax body

- GIVEN a broken package with a valid `p.Types` but a body-local type error
- WHEN SSA packages are created
- THEN the broken package has an `ssa.Package` with no function bodies
- AND callers of exported symbols in that package remain resolvable

### Requirement: Synthetic-node deletion is removed unconditionally

The system MUST NOT call `cg.DeleteSyntheticNodes()` after `cha.CallGraph`,
under any build condition (clean or partial). On a fully clean tree, the
resulting edge set MUST be identical to the edge set produced when synthetic
nodes were deleted.

#### Scenario: Clean-tree edge-set parity

- GIVEN a fully clean tree with no broken packages
- WHEN the call graph is built without `DeleteSyntheticNodes`
- THEN the resulting set of `(caller_qname, callee_qname)` pairs is identical
  to the golden edge set captured before this change

#### Scenario: Broken-package in-edges survive

- GIVEN a broken package that is called by several clean packages
- WHEN the call graph is built without `DeleteSyntheticNodes`
- THEN in-edges into the broken package's stub are present in the collected
  edge set (not silently dropped as they would be if synthetic nodes were
  deleted)

### Requirement: Edge-only carry-forward for broken-package callers

The system MUST carry forward edges from the previous `graph.db` only when
the edge's CALLER symbol belongs to a broken package. Carried edge endpoints
MUST be remapped from old symbol id to new symbol id by qname; an edge whose
caller or callee qname has no match in the fresh symbol set MUST be dropped,
not carried with a stale id.

#### Scenario: Caller-in-broken-package edge is carried

- GIVEN a previous `graph.db` containing an edge whose caller qname is in a
  package that is broken in the current build
- WHEN carry-forward runs
- THEN that edge appears in the fresh graph with both endpoints remapped to
  current symbol ids

#### Scenario: Deleted symbol drops its carried edge

- GIVEN a previous `graph.db` edge whose caller qname no longer exists in
  the fresh symbol set
- WHEN carry-forward runs
- THEN that edge is not present in the fresh graph

### Requirement: Cross-unit edges are never carried

The system MUST NOT carry forward an edge whose caller is in a clean
package and whose callee is in a broken package.

#### Scenario: Clean caller into broken callee is not carried

- GIVEN a previous `graph.db` edge whose caller qname is in a clean package
  and whose callee qname is in a broken package
- WHEN carry-forward runs
- THEN that edge is absent from the fresh graph even though it existed in
  the previous build

### Requirement: Carry-forward is strictly best-effort

The system MUST treat any error while opening, querying, or scanning the
previous `graph.db` during carry-forward as zero carried edges, and the
build MUST still succeed and write a fresh `graph.db`.

#### Scenario: Previous graph.db predates the dispatch column

- GIVEN a previous `graph.db` written before the `dispatch` column existed
- WHEN carry-forward attempts to read edges including `dispatch`
- THEN the read fails with a schema error
- AND carry-forward yields zero carried edges
- AND the build completes and writes a fresh `graph.db`

#### Scenario: No previous graph exists

- GIVEN this is the first-ever build for a repo whose tree currently has a
  broken package
- WHEN carry-forward runs
- THEN zero edges are carried
- AND the build still succeeds, with fresh symbols for every package

### Requirement: Greater-than-50%-broken safety cap

The system MUST serve the previous whole graph, unchanged, when more than
half of the loaded packages are broken.

#### Scenario: Majority of packages broken

- GIVEN a tree where more than 50% of loaded packages have `len(p.Errors) > 0`
- WHEN `buildIndex` runs
- THEN no fresh `graph.db` is written
- AND the previously persisted `graph.db` continues to be served
- AND the served graph's `Freshness.Stale` is `true`

### Requirement: Closure-precondition failure serves the same degraded state as the safety cap

A failed reachable-import-closure precondition MUST result in the identical
outcome as the greater-than-50%-broken cap: the previous whole graph is
served and `graph.db` is not rewritten. The system MUST NOT distinguish
these two failure causes as different response states.

#### Scenario: Closure failure and majority-broken produce the same served state

- GIVEN one build run where the closure precondition fails and a separate
  build run where more than 50% of packages are broken
- WHEN each build completes
- THEN in both cases the previous `graph.db` is served unchanged with
  `Freshness.Stale: true`, and no new persisted response field distinguishes
  the two causes

### Requirement: No SSA panic is ever asserted by a test

Tests verifying the closure precondition MUST call the precondition
function directly against a hand-built created-package set and MUST NOT
construct an incomplete `ssa.Program` and call `prog.Build()`.

#### Scenario: Precondition tested without invoking Build

- GIVEN a test asserting closure-precondition rejection
- WHEN the test constructs a created-package set missing a reachable import
- THEN it calls the precondition function directly and asserts a non-nil
  error
- AND the test never calls `prog.Build()` on that incomplete set

### Requirement: Package-variant deduplication with Tests enabled

When `Tests: true` causes `packages.Load` to return multiple variants for
the same `PkgPath` (the in-package test variant re-parsing the same
production files), the system MUST retain exactly one variant per `PkgPath`
for symbol-row emission — the in-package test variant when present,
otherwise the plain variant — and MUST skip synthesized `<pkg>.test` main
packages. SSA packages MUST still be created for every loaded package and
dependency, including all variants.

#### Scenario: No duplicate qname rows after dedupe

- GIVEN a package with both a plain variant and an in-package test variant
  under `Tests: true`
- WHEN symbol rows are emitted
- THEN no `qname` appears more than once in the symbol table
- AND a symbol lookup for any symbol in that package resolves unambiguously

### Requirement: Freshness reports carried units, capped

`Freshness` MUST expose which units (packages) are riding on carried edges
from a previous build, capped at the first 5 entries plus a total count and
a hint to retrieve the full list, and MUST expose `stale: true` only for
genuine build failures (I/O errors, corrupt cache) — not for a partial
build that itself succeeded.

#### Scenario: Stale units capped in the response

- GIVEN a build where 213 units carry forward edges from the previous graph
- WHEN the freshness metadata is rendered
- THEN the response lists the first 5 carried unit names, a total of 213,
  and a hint to fetch the full list, and does not inline all 213

#### Scenario: Partial build is not marked stale

- GIVEN a build that partitions packages, carries some edges, and
  successfully writes a fresh `graph.db`
- WHEN `Freshness` is rendered
- THEN `stale` is `false`, because the build itself succeeded even though
  some units carry forward edges

### Requirement: Per-symbol carried flag

A queried symbol's response MUST include a `carried` flag derived by
checking whether that symbol's package is present in the build's set of
carried units.

#### Scenario: Symbol in a broken, carried-edge package

- GIVEN a symbol whose package had carried-forward edges in the most recent
  build
- WHEN that symbol is queried
- THEN the response's `carried` flag is `true`

#### Scenario: Symbol in a cleanly-built package

- GIVEN a symbol whose package built cleanly with no carried edges
- WHEN that symbol is queried
- THEN the response's `carried` flag is `false`

### Requirement: Test-file census lockstep with Tests: true

The stamp computation that determines whether a cached graph is stale MUST
include `_test.go` files in its file census when `Tests: true` is enabled
for the semantic tier, so that editing a test file moves the build stamp.

#### Scenario: Editing a test file invalidates the stamp

- GIVEN a built graph and a subsequent edit to a `_test.go` file that adds a
  new caller
- WHEN the stamp is recomputed
- THEN the stamp differs from the previous stamp, triggering a rebuild
