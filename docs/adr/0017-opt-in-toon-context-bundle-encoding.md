# 0017 — Opt-in TOON encoding for the context bundle browse tier

**Status**: Deferred — did not clear the measure-first gate under conservative assumptions (Phase 0 spike, 2026-06-17)
**Date**: 2026-06-17

> **Outcome — deferred, not built.** The Phase 0 measure-first gate
> (`internal/toon/spike_test.go`, retained as reproducible evidence) measured a
> pipe-delimited TOON v3.0 browse-tier codec against JSON on a representative,
> prose-heavy corpus:
>
> | rows | json_tk | toon_tk | payload win | net win (incl. 120-tok prompt tax) |
> |------|---------|---------|-------------|-------------------------------------|
> | 5    | 376     | 311     | 17.3%       | **−14.6%** |
> | 10   | 750     | 608     | 18.9%       | **2.9%** |
> | 20   | 1501    | 1203    | 19.9%       | **11.9%** |
>
> T1.0 round-trip passed (realistic + adversarial), so the codec is *correct* —
> it simply doesn't *pay enough* under these assumptions. The 120-rune prose
> snippet forces quoting on nearly every row, capping the payload win at ~20%
> (the published 30–60% is for short-scalar arrays). Net win is 11.9% at max
> bundle size, negative at small ones — below the 20% bar.
>
> **Why Deferred, not Rejected.** The verdict is assumption-sensitive: (1) prompt
> tax is charged once *per bundle* here, but if amortized once per session it
> trends to ~0 and net ≈ payload ≈ 20% (at the bar); (2) the rune/4 heuristic is
> approximate — not the real Claude tokenizer; (3) the corpus is synthetic. The
> spike is strong enough to say "don't build it now," not strong enough for a
> permanent reject.
>
> **Before reconsidering, tighten the measurement** (deferred follow-up): rerun
> against the real Claude tokenizer, a real corpus sample, and an *amortized*
> prompt-tax model. **Revisit if** that tightened measurement clears the bar, or
> if the browse tier ever drops the prose snippet (short-scalar rows only), or
> the model's native TOON fluency + a stabilized spec change the economics.
>
> The decision below is the *deferred* design, recorded intact for that revisit.

## Context

The Context bundle is the largest read `droids-mem` returns, and for a consumer
like Claude Code it is injected straight into the model's context window every
Run — so its token cost competes directly with the user's working context.
Reducing that cost is the highest-leverage output-side optimization available.

TOON (Token-Oriented Object Notation) is a compact, tabular encoding for
**uniform arrays**: it emits the field header once and each record as a row,
saving the per-object key repetition that JSON pays. Its win is real but
**conditional** — it lands on arrays of flat scalar fields and degrades, or
even loses, on free-text fields whose commas, quotes, and newlines force
escaping/quoting.

Two constraints shape the design:

- The CLI contract is **hard-JSON** (JSON to stdout, error envelope to stderr),
  the MCP tools `json.Marshal` everything, and both e2e suites assert JSON.
  Replacing JSON would detonate every consumer, the error contract, and the
  tests.
- The bundle is heterogeneous:
  `{ task_type, last_session?, user_rules[], user_rules_total, browse[] }`. The
  **browse tier** is an array of uniform objects (`id, title, kind, tier,
  task_type, score` + a 120-rune snippet) — a good TOON fit. The **always
  tier** (`last_session`, `user_rules[]`) is full `learned` prose bodies —
  where TOON barely helps.

## Decision

**1. TOON is additive and opt-in; JSON stays the default contract.** Selected
explicitly per request — MCP `mem_context format=toon`, CLI `--format toon`.
Default output is unchanged, and the **error envelope is always JSON** regardless
of format. No existing consumer or test is affected.

**2. Hybrid scope — only the browse-tier array is TOON-encoded** as a table. The
envelope scalars and the always-tier prose bodies stay JSON. Whole-bundle TOON
is rejected: it spends format complexity on the prose parts where there is no
payoff.

**3. The encoder lives at the edge.** A new `internal/toon` package, called from
`cmd/output.go` and `internal/mcpserver`. The store stays struct/JSON and
format-agnostic; encoding is the **last step, post-scrub**, so it has zero
interaction with the safety pipeline. This mirrors the `internal/scrub` engine
isolation.

**4. Measure-first gate — net savings, not payload savings.** Before the format
ships, a spike measures browse-tier TOON vs JSON on representative real corpus
data. **Ship only if the *net* win clears ~20%.** Two effects can claw the win
back and must be in the measurement:
- **Prose-field quoting.** The 120-rune snippet usually contains a comma, forcing
  TOON to quote the value — surrounding quotes erase the per-comma saving. The
  published win (30–60% on short scalars) drops to an estimated ~15–25% once a
  long free-text column is present (toon-format benchmarks + arXiv 2603.03306).
- **Prompt tax.** The model may need instruction to read TOON reliably; the arXiv
  benchmark found this overhead *largely cancels* savings in short-context use.
  The spike measures the bundle *including* any instruction delta, not the payload
  in isolation.

A failing spike means the format does not ship — and given the two effects above,
not shipping is a live outcome, not a formality.

**5. No tokenizer dependency.** Measurement uses a rune/byte heuristic
(`≈ chars/4`). `tiktoken-go` is rejected: it is OpenAI's BPE, which mis-estimates
Claude by ~10–20%, it embeds multi-MB vocab files, and the dependency set is
locked. The same heuristic doubles as a `deep`-mode response-size budget guard
(a read-side trim — never an eviction; see ADR 0010 and ADR 0016).

**6. Bespoke pure-Go encoder targeting a pinned TOON v3.0 subset; no library
dependency.** The TOON spec is a **working draft (v3.0, 2025-11-24) that has
churned through four revisions in ~13 months**, and every Go implementation is at
v0.x, tracking that moving target. Taking such a dependency violates the locked,
pure-Go, minimal-dependency policy and couples the bundle format to an unstable
upstream. Instead `internal/toon` implements the small tabular subset we need
(~100–150 lines: header emission + per-row quoting + newline row separator),
**pinned to TOON v3.0**. Specifics:
- **Pipe (`|`) delimiter** for the browse tier — prose snippets contain commas far
  more often than pipes, so pipe minimizes mandatory quoting and preserves the
  win.
- **Conformance** is tested against the official `toon-format/spec` fixtures
  **pinned at a commit SHA** (vendored test vectors), not against a live library.
- **Versioning** is the envelope `toon_version` tag (point 1) plus the round-trip
  + fixture-conformance tests — deliberately *not* a scrub-style pinned-hash spec,
  because TOON output is ephemeral (never persisted on a row), so there is no
  migration or forensic reason to hash-lock it.

## Consequences

**Accepted**

- The JSON contract, both e2e suites, and the error envelope are untouched. TOON
  is a pure read-side option.
- The store remains unaware of output format; the new code is edge-only and
  isolated, like `internal/scrub`.
- The token win is captured exactly where the tokens are (the browse-tier
  scalar columns), without paying complexity on prose.

**Tradeoffs**

- Two output encodings exist for one endpoint; consumers that opt in must decode
  TOON.
- Snippet-field escaping **and prompt tax** may cap the net win — which is
  precisely why the measure-first gate exists; the feature may not ship.
- TOON v3.0 is an unstable working draft. Pinning a bespoke encoder to a v3.0
  subset + vendored fixtures isolates us from upstream churn, but a future spec
  revision the model is trained on may diverge from our pinned subset; revisit if
  the spec stabilizes or the model's native TOON dialect moves.

## Alternatives considered

- **Whole-bundle TOON** — rejected. No payoff on always-tier prose, more parse
  fragility for the same browse-tier win.
- **Replace JSON with TOON as the default** — rejected. Breaks the CLI/MCP
  contract, the error envelope, and both e2e suites.
- **Input-side TOON (agent writes memories in TOON)** — deferred. It puts a
  fragile parser in front of the scrub/fingerprint/dedupe seam for a weaker
  ("ease of writing") motive; output-side is where the token leverage is.
- **`tiktoken-go` exact token counting** — rejected. Wrong tokenizer for Claude,
  heavyweight, and against the locked dependency policy. A rune heuristic is
  sufficient for budgeting.

## Open items

- Exact net-win threshold and the spike's representative corpus (now includes
  prompt-tax accounting).
- Whether the `deep`-mode rune-budget trim ships with TOON or as a separate
  change.
- The exact `toon-format/spec` fixture commit SHA to vendor.
