# Code Graph Caller Fidelity Specification

## Purpose

Defines the reachability and honesty guarantees of the Go semantic graph's
caller answer: test-file callers MUST be indexed and counted, and dispatch
kind MUST be distinguishable, so an agent asking a blast-radius question is
never shown a caller count that hides most of the actual callers or hides
whether they are real invocations or CHA over-approximations.

## Requirements

### Requirement: Test symbols are indexed in the semantic tier

The system MUST load Go packages with `Tests: true` in the semantic tier's
`packages.Config`, so declarations inside `_test.go` files are indexed as
symbols and participate as callers in the call graph.

#### Scenario: A test-only caller is indexed

- GIVEN a function whose only caller is a `_test.go` test function
- WHEN the graph is built
- THEN a symbol row exists for that test function
- AND an edge exists from the test function to the callee

### Requirement: Caller counts are split production vs. test

A symbol's response MUST report the count of callers residing in
`_test.go` files as a distinct value from the total caller count, so
production and test callers are both visible and neither is hidden.

#### Scenario: Split count on a heavily test-called symbol

- GIVEN a symbol with 91 total callers, 5 in production files and 86 in
  `_test.go` files
- WHEN that symbol is queried
- THEN the response's total caller count is 91
- AND the response's test-caller count is 86

### Requirement: Distinct test-file count is a dedicated field

The response MUST report the number of distinct `_test.go` files containing
a caller as its own field, separate from the caller count and not embedded
in a prose string.

#### Scenario: Distinct file count reported

- GIVEN 86 test callers for a symbol spread across 19 distinct `_test.go`
  files
- WHEN that symbol is queried
- THEN the response includes a field whose value is 19, independent of the
  86-caller count field

### Requirement: Test-ness is derived from file path, no per-row origin column

The system MUST derive whether a caller is a test caller from its `loc`
file-path suffix (`_test.go`) rather than persisting a per-row origin
column. The query MUST use an escaped LIKE pattern so a literal underscore
in the suffix is not treated as a wildcard.

#### Scenario: Underscore in filename does not misclassify

- GIVEN a caller located in `helpertest.go` (no `_test.go` suffix) and
  another in `foo_test.go`
- WHEN test-ness is evaluated for each
- THEN `helpertest.go` is not classified as a test file
- AND `foo_test.go` is classified as a test file

#### Scenario: No per-row test/production column on the wire

- GIVEN a caller-list response
- WHEN the response is inspected
- THEN no individual caller row carries a per-row origin/test field; the
  caller's `loc` path is the only per-row signal, and split counts appear
  only at the response level

### Requirement: Neighbor ordering guarantees production callers are never truncated away by test callers

The neighbor list MUST be ordered `is_test` ascending first, then same-package
proximity (`s.package != ?`), then `s.qname` alphabetically. This ordering
MUST be applied before any cap is enforced, so that under a fixed cap,
production callers are always shown before test callers are shown, and the
existing same-package proximity clause continues to apply within each
`is_test` group.

#### Scenario: Production callers are not truncated by test volume

- GIVEN a symbol with 5 production callers and 86 test callers, and a
  neighbor cap smaller than 91
- WHEN the caller list is computed
- THEN all shown production callers precede all shown test callers in the
  ordered result
- AND no production caller is dropped from the shown set in favor of a test
  caller

#### Scenario: Same-package proximity preserved within each group

- GIVEN multiple production callers, some in the queried symbol's own
  package and some in other packages
- WHEN the caller list is computed
- THEN within the production group, same-package callers are ordered before
  other-package callers, consistent with the pre-existing proximity clause

### Requirement: Neighbor cap remains 50, a single tunable, no value-dependent branching

The maximum number of neighbors shown per query MUST remain 50, expressed
as a single tunable variable. No code path MUST branch its behavior based
on the specific value of this cap beyond truncating the shown slice length.

#### Scenario: Cap governs only the shown slice length

- GIVEN a symbol with more callers than the cap
- WHEN the response is built
- THEN the ordering, all split counts, `CallersTotal`/`CalleesTotal`, and
  the truncation hint are computed independently of the cap value
- AND only the length of the returned neighbor slice depends on the cap

### Requirement: Dispatch is labelled per edge and split at response level

Each collected edge MUST carry a `dispatch` label of `"static"` or
`"interface"`, derived from `callgraph.Edge.Site.Common().IsInvoke()` with
a nil-guard for edges with no `Site` (synthetic edges default to
`"static"`). The response MUST surface a response-level split count of
static vs. interface-dispatch callers. No per-row dispatch field MUST
appear on the wire.

#### Scenario: Interface dispatch counted separately

- GIVEN a symbol with 40 callers, 36 reached only via interface dispatch
  (e.g., an `.Error()`/`String()`/`Close()`-shaped method)
- WHEN that symbol is queried
- THEN the response reports a static-caller count and an interface-caller
  count summing to 40, with 36 attributed to interface dispatch

#### Scenario: Nil Site on a synthetic edge does not crash labelling

- GIVEN a synthetic edge with a nil `Site`
- WHEN the dispatch label is computed for that edge
- THEN the label resolves to `"static"` without a nil-dereference error

#### Scenario: No per-row dispatch field on the wire

- GIVEN a caller-list response
- WHEN the response is inspected
- THEN individual caller rows carry no per-row `dispatch` field; only the
  response-level split counts expose dispatch information

### Requirement: Dispatch-dominance hint

The response MUST include a hint when interface-dispatch callers exceed 50%
of the total caller count for that symbol, so an agent can distinguish a
CHA fan-out from a genuine hub without counting manually.

#### Scenario: Hint fires when interface dispatch dominates

- GIVEN a symbol with 40 callers where 36 (90%) are interface-dispatch
- WHEN that symbol is queried
- THEN the response includes a hint indicating interface dispatch dominates
  the caller list

#### Scenario: Hint does not fire below the dominance threshold

- GIVEN a symbol with 40 callers where 10 (25%) are interface-dispatch
- WHEN that symbol is queried
- THEN the response does not include the dispatch-dominance hint
