# Implementation plan — auto-session-summary + TOON

Phased build for ADR 0016 (native Claude Code session integration) and ADR 0017
(opt-in TOON browse-tier encoding), validated by `eval-plan.md`.

## Principles
- **Measure-first.** The TOON spike is a cheap kill-gate; run it before building
  any TOON infrastructure (ADR 0017).
- **Vertical slices.** Each phase ships something testable end-to-end.
- **Contract untouched.** JSON stays the default; error envelope always JSON; the
  4-tool MCP surface does not grow.
- **Store stays format- and hook-agnostic.** TOON encoding lives at the edge
  (`internal/toon`); Claude Code hooks live in `settings.json` (a CC adapter that
  versions separately from the binary), driving the binary's commands.
- **Two independent tracks.** Feature 1 (F1, definitely ships) and the TOON track
  (gated, may not ship) share no code — they can proceed in parallel.

## Track ordering at a glance
```
Phase 0  TOON spike (kill-gate) ────────────────► Phase 6 TOON build (only if gate passes)
Phase 1  origin schema ─► Phase 2 eviction ─┐
                         └─► Phase 3 recent-sessions CLI
                                              ├─► Phase 4 staging + flush + intake gate
                                              └─► Phase 5 CC hook adapter
                                                          └─► Phase 7 Tier-1 eval harness
```
Phase 0 and Phase 1 have no dependency — start both first.

---

## Phase 0 — TOON measure-first spike (kill-gate) · ADR 0017 — DONE (2026-06-17)
**Goal:** decide whether TOON ships at all, before building it.
- Built throwaway pipe-delimited v3.0 browse-tier codec
  (`internal/toon/spike_test.go`, retained as evidence).
- T1.0 round-trip: **PASS** (realistic + adversarial).
- T1.1 net-token: payload win ~20% (prose quoting caps it), **net win 11.9% at 20
  rows, negative below** — under a conservative 120-tok/bundle prompt tax.
- **Outcome: gate not cleared → ADR 0017 → Deferred.** Phase 6 is parked. The
  verdict is assumption-sensitive (per-bundle prompt tax, rune/4 heuristic,
  synthetic corpus), so it is a "don't build now," not a permanent reject.
- **Deferred follow-up (before any revisit):** tighten the measurement — real
  Claude tokenizer, real corpus sample, amortized prompt-tax model. Revisit Phase
  6 only if that clears the bar.

## Phase 1 — `origin` schema foundation · ADR 0016 (pt 4)
**Goal:** the column everything F1 hangs on.
- Add `origin TEXT NOT NULL DEFAULT 'manual' CHECK(origin IN ('manual','auto'))`
  to `memories` (new migration ladder step; schema version bump).
- Existing rows backfill to `'manual'` via the DEFAULT.
- Composite index `(origin, created_at)` (also serves Phase 3).
- **Tests:** migration up from a pre-column DB; CHECK rejects bad values.
- **Exit:** fresh + migrated DBs both carry `origin`; all existing suites green.

## Phase 2 — origin-keyed value-aware eviction · ADR 0016 (pt 6)
**Goal:** auto-summaries evict on their own budget, by proven value.
- Extend the inline `session_summary` ring buffer: for `origin='auto'`, global
  newest-**M=30** budget (separate from manual per-task_type ≤5).
- Eviction precedence (K=5 grace, LRU on `last_expanded_at`) per ADR 0016 pt 6;
  runs inline in the save transaction, no background ticker.
- **Tests:** precedence unit tests (never-expanded-oldest → expanded-coldest →
  oldest-overall); K<M honored; manual retention untouched.
- **Exit:** auto saves hold the corpus at ≤M; manual ≤5-per-task_type unaffected.

## Phase 3 — `recent-sessions` CLI read path · ADR 0016 (pt 7)
**Goal:** human/operator recency view (off MCP).
- CLI `droids-mem recent-sessions --limit N` →
  `WHERE origin='auto' ORDER BY created_at DESC LIMIT N` (uses Phase-1 index).
- JSON out per CLI contract.
- **Tests:** ordering, limit, empty-corpus.
- **Exit:** command returns recency-ordered autos; not added to MCP.

## Phase 4 — staging + flush + intake gate · ADR 0016 (pts 2, 3, 5)
**Goal:** the core save mechanism, driven by binary commands (hooks come next).
- `state` pkg: sentinel files under `~/.droids-mem/` — staged payload (keyed by
  CC session id, carries optional droids-mem `session_id`) + change counter.
- `droids-mem stage` subcommand: write/replace the staged summary file.
- Flush path: read staged file → `store.Save` (origin='auto', task_type
  `claude_session`), reuse staged `session_id` or mint.
- Intake gate: counter ≥ N=3 AND a staged payload present, else flush no-ops.
- Recovery flush: on session start, flush an orphaned staged file then clean it;
  7-day TTL backstop for unflushable files.
- **Tests:** stage→flush round-trip; gate blocks low-value; recovery flush;
  session_id reuse-vs-mint; eviction (Phase 2) fires on auto flush.
- **Exit:** a staged summary flushes to a correct auto-summary row; gate + recovery
  proven without any hook wiring.

## Phase 5 — Claude Code hook adapter (`settings.json`) · ADR 0016 (pts 2, 8)
**Goal:** wire the binary commands to CC events; the CC-specific adapter.
- `PostToolUse` → bump change counter.
- `Stop` → threshold-gated checkpoint: at/over N, block once + inject "stage now".
- `SessionEnd` → flush staged summary.
- `SessionStart` → recovery flush.
- `UserPromptSubmit` → score-floored `mem_search`, inject above floor, dedupe
  once-per-memory-per-session (relevance-gated pull).
- Skill/prompt material for composition + the model-judgment half of the gate.
- **Tests:** hook scripts unit-tested against synthetic payloads; manual CC
  end-to-end smoke (hooks can't run in Go CI).
- **Exit:** a real Claude Code session auto-stages at checkpoints, flushes at end,
  surfaces relevant prior sessions on prompt.

## Phase 6 — TOON build (PARKED — Phase 0 gate not cleared) · ADR 0017
**Goal:** ship the validated encoder. **Blocked:** unpark only if the tightened
re-measurement (Phase 0 follow-up) clears the net-win bar.
- `internal/toon`: pinned v3.0 tabular subset, pipe delimiter, ~100–150 LOC.
- Wire `format=toon` into MCP `mem_context` + `--format toon` CLI; envelope
  `toon_version` tag; error envelope stays JSON.
- Vendor `toon-format/spec` fixtures at a pinned SHA; T1.0 round-trip in CI.
- **Exit:** opt-in TOON browse tier, JSON default untouched, fixtures green.

## Phase 7 — Tier-1 eval harness · eval-plan.md
**Goal:** defensible, CI-able numbers.
- Implement T1.0–T1.6 over the frozen corpus + labeled sets in `testdata/eval/`.
- Emit the JSON metrics report as a CI artifact.
- **Exit:** Tier-1 runs in CI; bars ratcheted from the first baseline.

---

## Deferred (separate workstreams, not in this plan)
- **Tier-2 eval** (task success, net token ROI, relearning) — needs a task corpus
  + grader; its own project.
- **Dual-pool readers** (`db.go`, Future.md) — only if in-process concurrent agent
  traffic proves heavy (D-eval will show it).
- **`mem_recent_sessions` MCP tool** — only if evidence shows the agent needs
  recency-ordered mid-run lookback that `mem_search` can't serve.

## Housekeeping (any time)
- "See also 0016" cross-refs in ADR 0004 (enum) + ADR 0010 (retention).
- Refresh the stale ADR index in `CLAUDE.md` (stops at 0011; 0012–0017 exist).
- Flip ADR 0016 / 0017 `Status: Proposed → Accepted` when their first phase lands.
