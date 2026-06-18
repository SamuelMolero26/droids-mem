# droids-mem evaluation plan

Quantifies the impact of the v-next update (auto-session-summary, ADR 0016;
opt-in TOON browse-tier encoding, ADR 0017) on agent consumers such as Claude
Code.

Two tiers. **Tier 1** is deterministic, offline, CI-able, and gates ship.
**Tier 2** is LLM-in-the-loop, periodic, and produces the headline value story.
Pass bars below are initial targets — tune against the first real run.

> **Implementation status caveat.** ADR 0016 and ADR 0017 are *Proposed*. Several
> code paths these evals assume are not yet merged: the `internal/toon` package,
> the `origin` column + value-aware (Expand-signal) eviction (ADR 0016), and the
> `recent-sessions` auto-summary read path. Each eval below states the dependency
> it is blocked on. Do not treat an eval as a ship gate before its dependency
> lands — an unimplemented feature cannot be measured.

---

## Tier 1 — deterministic (no model calls, runs in CI)

### T1.0 TOON round-trip correctness — the real TOON ship-gate
- **Question:** does the TOON codec preserve the bundle exactly? A token win on a
  bundle the decoder misparses is a regression, not a feature.
- **Method:** property-based test — take a `ContextResponse`, `encode()` to TOON,
  `decode()` back, assert **field-for-field equality** (titles, snippets, tiers,
  `user_rules_total`, every browse row). Stress the snippet-escaping risk called
  out in ADR 0017: rows containing delimiters, commas, quotes, newlines, and
  unicode.
- **Fixture:** the frozen corpus (below) plus an adversarial escaping corpus.
- **Pass bar:** **100%** round-trip equality on the full corpus. Any mismatch →
  TOON does not ship, regardless of token win.
- **Blocked on:** `internal/toon` package (not yet implemented).

### T1.1 Token efficiency — the TOON token win
- **Question:** how many tokens does the browse tier cost in TOON vs JSON?
- **Method:** render a representative bundle's browse tier both ways; count with
  the rune heuristic (`≈ chars/4`, ADR 0017 — no `tiktoken`). Report per-mode
  (`orient`, `deep`) and per-corpus-size. **Heuristic is inaccurate for Claude
  BPE (±10–20%, worse on code snippets); spot-check a 20-row sample against the
  real Claude tokenizer and report the delta** so the gate isn't decided by
  measurement error alone.
- **Fixture:** a frozen corpus snapshot with a spread of snippet lengths AND
  shapes (short scalar rows → long prose → snippets with commas/quotes, the field
  that can claw back the win per ADR 0017).
- **Pass bar:** TOON browse-tier win **≥ 25%** by the heuristic (5-point margin
  over the ADR-0017 ~20% target, to absorb heuristic error). Below bar → TOON
  does not ship. Gate is **conditional on T1.0 passing first.**

### T1.2 Recall@k — does the bundle surface the right memory?
- **Question:** given a `task_type`+query with a known-relevant memory, is it in
  the bundle — and did *ranking* put it there, or forced inclusion?
- **Method:** labeled `(task_type, query, relevant_mem_id)` triples; assemble the
  bundle. Split into two sub-metrics — the always tier is forced inclusion
  (`user_rule`/`last_session` fetched by recency, no BM25), so folding it into one
  recall number lets the metric pass without BM25 doing any work:
  - **`browse_recall@k`** — is `relevant_mem_id` in the **top-k BM25-ranked
    browse-tier items**, excluding always-tier forced rows and ADR-0011 rule
    stubs? This is the only number that evals retrieval quality.
  - **`always_tier_coverage`** — are the expected forced-in rows present?
  - Report `browse_recall@5`, `browse_recall@10`, tier placement.
- **Fixture:** labeled retrieval set built from **genuine task transcripts**, not
  authored with knowledge of the BM25 column weights / tokenizer (avoids
  overfitting the eval to the implementation).
- **Pass bar:** `browse_recall@10 ≥ 0.85` (initial). `always_tier_coverage = 1.0`.

### T1.3 (B) Cross-session continuity — evals the auto-session-summary
- **Question:** does a later Run inherit an earlier Run's key decisions? (The
  only metric that evals ADR 0016.)
- **Routing constraint:** `fetchLastSessionConn` (`context.go`) filters
  `WHERE task_type = ? AND kind = 'session_summary'`. The current `Context()`
  bundle **cannot** surface an auto-summary saved under a different `task_type`.
  Cross-workflow recall requires the ADR-0016 `WHERE origin='auto' ORDER BY
  created_at DESC LIMIT N` read path, which is not in `Context()` yet. Scope
  accordingly:
  - **In scope now:** same-`task_type` continuity through `Context()`.
  - **Blocked on `recent-sessions` read path:** cross-`task_type` continuity.
- **Method:** session pairs `A → B`. Save A's auto-session-summary through the
  full intake gate; assemble B's bundle for the **same** `task_type`; check
  whether A's key facts (labeled **from A's original transcript**, not from what
  the auto-summary chose to keep — avoids inflation) are carried. Report
  **continuity hit rate**. Also assert the intake gate: low-value A (below N=3,
  no model stage) yields **no** auto-summary.
- **Fixture:** labeled session-pair set, including negative (quiet) sessions.
- **Pass bar:** continuity hit rate **≥ 0.90** on qualifying same-`task_type`
  pairs; **0** false auto-summaries on quiet sessions. (Bar is fragile at small N
  — report the pair count alongside; ±1 pair must not flip ship.)

### T1.4 (D) Latency / throughput under concurrency
- **Question:** is the bundle fast, and does concurrent agent traffic lock the DB?
- **Method:** micro-bench bundle assembly (`orient`/`deep`) and save; measure
  p50/p95. Concurrency harness: **N=8 readers + 1 writer** against one WAL DB;
  assert no `SQLITE_BUSY` escapes `busy_timeout`. Ties to the WAL / dual-pool
  thread (`db.go`, Future.md).
- **Environment (required — numbers are meaningless without it):** GitHub Actions
  `ubuntu-latest` 2-core runner, DB on the runner's local disk. Re-baseline if CI
  infra changes.
- **Pass bar (initial):** bundle p95 `orient` < 25 ms, `deep` < 75 ms on the
  stated runner; zero unhandled `SQLITE_BUSY` under the concurrency harness.

### T1.5 (E) Dedupe + scrub precision/recall
- **Question:** does dedupe suppress real duplicates without eating new signal,
  and does scrub catch secrets without over-redacting?
- **Method:**
  - Dedupe: labeled near-dup / distinct pairs → report **dup-catch rate** and
    **false-skip rate** (real new memory wrongly suppressed). Fixture **must**
    cover the ADR-0001 `what`-excluded-from-fingerprint boundary:
    - (a) identical `title+learned+kind+task_type`, different `what` → assert
      layer-1 fingerprint suppresses the second save.
    - (b) identical `what`, different `learned` → assert layer-1 passes, falls to
      layer-2 Jaccard.
    - (c) Jaccard 0.84 (just below) vs 0.86 (just above) → assert threshold holds.
  - Scrub: **extend the existing `RunCorpus`** harness → report **secret-catch
    rate** and **false-positive rate**.
- **Pass bar (initial):** dup-catch ≥ 0.95, false-skip ≤ 0.02; scrub
  secret-catch ≥ 0.99, **false-positive ≤ 0.01** (save is on the session-end hot
  path — a 5% reject rate surfaces weekly; 1% is still generous).

### T1.6 (F) Storage efficiency
- **Question:** how does the corpus grow?
- **Method:** report DB bytes per memory, bytes growth per session, % saves
  deduped. Observational; ties ADR 0010 size handling.
- **Pass bar:** none (tracked). Flag if bytes/memory regresses > 25% release over
  release.
- **Deferred until ADR 0016 lands:** "auto-summary budget occupancy (newest-M)"
  is **not measurable yet** — `pruneSessionSummariesConn` is recency-only
  (`ORDER BY created_at DESC LIMIT 5`); the `origin` column, global-M ring, and
  Expand-signal eviction are not implemented. Add the occupancy metric only after
  that code merges.

---

## Tier 2 — LLM-in-the-loop (periodic, headline value)

Needs a Claude Code task suite, a runner, and a calibrated grader. Not CI-gating.

**Rigor preconditions (lock before any Tier-2 run):**
- **Judge calibration** — grade 20 hand-labeled cases; require **≥ 90% agreement**
  with ground truth before the judge is trusted.
- **Judge blinding** — give the judge only the agent's **output actions + terminal
  file/code state**, never the transcript containing the injected bundle (a
  capable judge trivially infers the arm from bundle presence).
- **Minimum N** — **≥ 30 task-runs per arm per task variant**; report 95% CIs on
  every Tier-2 metric.
- **Task-suite calibration** — pilot first; confirm there exist tasks where
  mem-on visibly beats mem-off. Tasks too trivial (re-derived in 2 turns) or too
  hard (fail regardless) both yield Δ≈0 with no power to interpret it.

### T2.1 End-task success — with vs without droids-mem
- **Question:** does memory make the agent complete tasks better/faster, and does
  it ever *mislead*?
- **Method:** A/B the same task suite across arms: mem-off, mem-on (JSON),
  mem-on (TOON), and a **negative-control arm** that injects a deliberately
  **stale/wrong** memory (an `error_resolution` whose fix was later reverted, or a
  superseded `user_rule`). Grade success via rubric + **blinded** LLM judge.
  Report success-rate delta and turns-to-completion delta per arm.
- **Pass bar:** report Δ with CIs (no fixed bar v1); target positive success
  delta for mem-on, **and no significant regression on the negative-control arm**
  (if stale memory measurably hurts, ADR 0016 needs staleness/retention work
  before ship).

### T2.2 Token cost ceiling — measurable ROI proxy
- **Question:** is the injection cost paid back by faster completion?
- **Method:** use only **measured** quantities — no counterfactual "tokens not
  re-derived." Compute `injection_tokens_per_session` (instrumented bundle
  payload) and compare against `Δ(turns_to_completion) × mean_tokens_per_turn`
  from the mem-off arm.
- **Pass bar:** `injection_tokens_per_session ≤ Δturns × tokens_per_turn`
  (injection cost recovered by turn savings) on the task suite. Falsifiable by
  experiment, unlike a net-ROI figure built on unmeasurable savings.

### T2.3 Relearning-events avoided
- **Question:** does the agent stop re-deriving what it already learned?
- **Method:** annotate task runs for "relearning events" against a **pre-locked,
  annotator-facing definition with examples + counter-examples** (e.g. re-solving
  the *same* known bug counts; solving a related-but-distinct bug does not).
  Annotation is **blinded** — the annotator sees agent output only, not which arm
  produced it. Compare counts with vs without memory.
- **Pass bar:** **≥ 50% reduction** in relearning events on tasks with a relevant
  prior memory — where 50% is **re-derived from the pilot's mem-off baseline
  count**, not assumed. Run the pilot before fixing this number.

---

## Shared harness / fixtures
- **Frozen corpus snapshot** — one versioned DB fixture drives T1.0, T1.1, T1.2,
  T1.6; regenerated deliberately, not per-run, so token/recall numbers are
  comparable across releases. Must span snippet lengths AND shapes (prose,
  commas/quotes, unicode) plus cross-`task_type` variety.
- **Labeled sets** — retrieval triples (T1.2, from real transcripts), session
  pairs (T1.3, facts from A's original transcript), dedupe pairs incl. the
  ADR-0001 four-quadrant cases (T1.5). Live under `testdata/eval/`.
- **Isolation** — every eval overrides `DROIDS_MEM_DB` + `DROIDS_MEM_HOME`, like
  the existing e2e suites.
- **Reporting** — Tier 1 emits a JSON metrics report (CI artifact, diffed across
  releases). Tier 2 emits a run report with per-task traces and 95% CIs for audit.

## Open items
- Final pass bars after first baseline run.
- Task-suite corpus selection for Tier 2 (representative Claude Code workflows),
  gated by the pilot calibration above.
- Grader/rubric design for T2.1, locked **before** the task suite is built.
- ADR-0016 `recent-sessions` read path — required before cross-`task_type` T1.3.
