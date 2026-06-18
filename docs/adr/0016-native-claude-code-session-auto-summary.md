# 0016 — Native Claude Code session-end auto-summary

**Status**: Proposed
**Date**: 2026-06-17

## Context

Until now the only modeled consumer of `droids-mem` was `droids-agents` on
agentspan (ADR 0004): a Root agent acts as the sole writer and explicitly
composes a Rollup at the end of an Execution. Claude Code is a different
consumer shape — there is no separate Root broker; **Claude Code is the sole
agent, so it is its own Root.** Nothing in the runtime guarantees that a
`session_summary` is written before a Claude Code session ends. Lessons from a
productive session are silently lost when the user runs `/clear`, quits, or the
terminal closes without the model having decided, on its own, to call
`mem_save`.

A pure prompt protocol ("before you finish, save a summary") is best-effort:
the model forgets, and abnormal exits escape entirely. We want a path that is
*enforced*, not merely *requested* — but enforcement runs into Claude Code's
hook semantics:

- `SessionEnd` fires at the true session boundary but its output is ignored —
  **it cannot block and cannot invoke the model**, so it cannot author a
  summary at exit.
- `Stop` fires at the end of **every** assistant turn and *can* block (it
  re-prompts the model), but blocking every turn nags and risks loops.
- The store is **single-writer SQLite**; any per-turn work on the hot path must
  be DB-free and model-free to avoid cold-spawn tax and write contention with
  the `BEGIN IMMEDIATE` dedupe transaction.

So "guarantee a session-end save" is not a single hook — it is a split between
a deterministic enforcement path and a model-authored composition path, plus a
gate that keeps low-value sessions out.

The same best-effort-vs-guaranteed problem exists symmetrically on the **read**
side: the agent is supposed to pull prior sessions about the current task, but
nothing forces it to. So this ADR covers both directions of the Claude Code
integration — **write enforcement** (checkpointed auto-summary, decision points
1–6) and **read enforcement** (relevance-gated recall, decision point 8) — with
the recency view left as a human/operator concern (decision point 7).

## Decision

**1. Run boundary = the whole Claude Code session.** One Claude Code session
produces at most one Auto-session-summary, consistent with the existing
one-Run-one-`session_summary` invariant.

**2. Enforce-and-compose via a staged summary, checkpointed at stopping points.**
The model composes a complete `mem_save` payload *during* the run and writes it
to a file (the **staged summary**). `SessionEnd` flushes that file to
`store.Save`. The `Stop` hook (which fires at every turn end — each a natural
stopping point) does a DB-free, model-free check: are the meaningful changes
since the last stage ≥ the intake threshold (N = 3)?

- **Below threshold** → silent pass. No nag, no model work on quiet turns.
- **At/over threshold** → **block once and inject "stage your progress now"**, so
  the model (re)stages an updated summary at that stopping point, then unblocks.

This makes staging an **enforced checkpoint at every stopping point where real
work accumulated**, not a single end-of-run gamble. The block fires **once per
threshold-crossing**, never until-saved-every-turn, to avoid the Stop-hook nag
loop. `SessionEnd`'s inability to invoke the model stops mattering because the
content was already staged at the latest checkpoint.

**Crash durability via recovery flush.** Because staged summaries are files, a
run that dies before `SessionEnd` leaves its last checkpoint on disk. On the next
session start, an orphaned staged file from a crashed run is **flushed to the DB
and then cleaned** — recovered, not discarded. The latest enforced checkpoint
survives a crash.

**3. Staging lives in files, not the DB and not `force`-save.** Staged
summaries and the change counter live as sentinel files under `~/.droids-mem/`
(owned by the `state` package), keyed by the Claude Code session id from the
hook payload. A `PostToolUse` hook touches the change marker on Edit/Write/Bash.
This keeps the per-turn hot path DB-free, keeps the draft concept out of the
data model, and avoids overloading `force` (which is HITL correction only).

The staged payload **carries the droids-mem `session_id`** captured at compose
time (the `stage` command takes an optional `session_id`, mirroring `mem_save`).
At flush: reuse the staged `session_id` if present, otherwise mint a fresh one.
This preserves the Session invariant — the auto-summary joins the same Session
grouping as any manual saves in the Run rather than splitting into a disjoint
one — and rides the existing agent-owned session model (ADR 0003) with no
side-map. Session **resume** is best-effort: if Claude Code reuses the same
session id, the staged file persists and composition continues; the droids-mem
`session_id` continuity then depends on the agent threading the prior id per the
ADR 0003 contract, not on new machinery.

**4. Differentiation is a column, not a new Kind. The 4-kind enum stays frozen
(ADR 0004).** An Auto-session-summary is `kind: session_summary` distinguished
by a new column:

```sql
origin TEXT NOT NULL DEFAULT 'manual' CHECK(origin IN ('manual','auto'))
```

`origin` is orthogonal to Kind and Scope — it records *how* a Memory was
authored, not what it means. Auto-session-summaries carry `origin: 'auto'` and a
reserved `task_type: 'claude_session'` (cosmetic default only — see point 6, it
no longer drives retention).

**5. Intake gate (locked).** An Auto-session-summary is persisted only when
**both** conditions hold:

- **Mechanical pre-filter:** the change counter ≥ **N = 3** meaningful tool
  calls (Edit/Write/Bash) *or* any `git commit`, counted DB-free in the sentinel
  file via the `PostToolUse` hook.
- **Model judgment:** the model staged a summary because the Run was worth
  recalling. It may decline even above threshold (a busy but fruitless run).

A session failing either condition produces no Auto-session-summary. This is the
primary defense against retaining irrelevant sessions — junk is kept *out at
intake* rather than cleverly evicted later.

**6. Retention is inline and value-weighted, never a background ticker.**
Auto-session-summaries extend the existing inline `session_summary` ring buffer
(today: "on save, delete oldest if > 5 for that task_type"), but keyed on
`origin` rather than `task_type`:

- Autos get a **global per-origin budget — newest M = 30**, separate from the
  manual per-task_type ≤ 5 budget, so machine-forced writes never evict curated
  summaries. M is a hardcoded constant for v1 (consistent with the hardcoded tier
  sizes); a `DROIDS_MEM_AUTO_BUDGET` env override is deferred until the storage
  eval (T1.6) shows real session-cadence variance demands it.
- Eviction is **value-aware, reusing the Expand signal (ADR 0013)**, with a
  **cold-start grace of K = 5** (K strictly < M). On each auto save where
  `count(origin='auto') > M`, evict in this precedence:
  1. **never-expanded AND outside newest-K** → evict oldest by `created_at` first;
  2. still over M → **expanded, outside newest-K** → evict the coldest by
     `last_expanded_at` (LRU — recency tracks ongoing relevance better than
     lifetime frequency for time-bound summaries);
  3. still over M (everything inside newest-K) → evict oldest overall.
  M is the hard cap; K is a best-effort shield, not an absolute veto — so a
  brand-new valuable summary is protected from value-eviction but cannot keep the
  corpus above M. Value is *measured usage*, never a guessed importance score.
- Eviction runs **inline in the save transaction**, not in a background
  goroutine. A size-triggered background purge is explicitly rejected — it would
  violate ADR 0010 (no automatic retention of the corpus) and add a concurrent
  writer to single-writer SQLite.

**7. Cross-session recall splits by audience: relevance for the agent, recency
for the human.** A recency-ordered "last N runs" list is the wrong abstraction
to feed an agent — it surfaces by *when*, not by *applicability to the current
task*, so pushing it into context injects often-irrelevant noise. Therefore:

- **The agent gets auto-summaries through the existing relevance-gated surfaces
  only — no new MCP tool, no SessionStart push. MCP stays at 4 tools.** The flow
  is `mem_search(query, kind=session_summary)` → `mem_get(id)`: the agent pulls
  *prior sessions about X* when it starts work on X, not a recency dump. Auto-
  summaries are ordinary FTS rows, so search finds them by relevance regardless
  of their `claude_session` bucket; `mem_context` still gives same-task
  continuity via `last_session`.
- **`mem_get` closes the retention loop:** it increments the Expand signal
  (ADR 0013), which is the same value axis the eviction in point 6 uses. A
  frequently-pulled auto-summary proves its value and is protected; a never-
  pulled one ages out. Retrieval and retention reinforce each other with no extra
  machinery.
- **Recency recall is a CLI/operator concern only.** `WHERE origin='auto' ORDER
  BY created_at DESC LIMIT N` (composite index `(origin, created_at)`, never
  touches FTS5 or the `mem_context` hot path) is exposed as a CLI
  `recent-sessions` command for a human reviewing their own history — consistent
  with operator commands (`list`/`doctor`/`prune`/`tui`) being intentionally off
  MCP.

**8. Relevance-pull enforcement — guarantee prior sessions about X surface when
the agent starts work on X.** Symmetric to the save-side enforcement: the
`mem_search → mem_get` recall flow is otherwise best-effort (the agent must
remember to search). A **`UserPromptSubmit` hook** closes that hole:

- On each user prompt it runs a **score-floored `mem_search`** over the prompt
  text and injects a hit **only when it clears a relevance floor** (FTS rank /
  Jaccard minimum). A match → the applicable prior session is surfaced,
  guaranteed; no match above floor → **inject nothing.** This handles both cases
  the consumer asked for: pull when a session about X exists, stay silent when
  none does.
- The floor is **mandatory** — BM25 always returns a top row, so without a
  minimum-relevance gate weak junk would be injected and recreate the
  irrelevant-context problem. Injections are **deduped — a given memory is
  injected at most once per session** — so re-prompts do not re-spam it.
- `UserPromptSubmit` is the trigger because relevance retrieval needs the query,
  the query first exists at prompt submit, and it fires *before* the agent acts.
  Firing on **every** prompt (not just the first) is deliberate: it catches
  **mid-session topic pivots** ("now work on Y" surfaces prior-Y sessions). The
  floor + dedupe keep it silent and sub-millisecond when nothing applies.

This is **relevance-gated push** — distinct from the rejected recency push
(decision point 7): it fires only on a real, scored match, so applicability is
the gate, not recency.

## Consequences

**Accepted**

- The 4-kind enum, dedupe, FTS5 index, and context-bundle tier logic are
  unchanged. Differentiation costs exactly one nullable column plus one
  composite index.
- ADR 0004 (enum freeze) and ADR 0010 (no automatic corpus retention) both hold:
  no fifth kind, and auto-eviction is the pre-existing `session_summary`
  ring-buffer exception, not background corpus GC.
- The Expand signal (ADR 0013) gains a second consumer (eviction value axis)
  beyond browse-tier sizing.
- The per-turn hot path stays DB-free and model-free; the only DB write is the
  single `SessionEnd` flush.

**Tradeoffs**

- Enforcement is Claude-Code-specific: the hooks live in `settings.json`, not in
  the binary, so this half is an adapter that versions separately from core.
- The guarantee is strong, not absolute: `kill -9` / crash / terminal close may
  skip `SessionEnd`. Accepted — a lost summary for a hard-killed session is
  tolerable.
- Retention logic now branches on `origin` (autos: global newest-M value-aware;
  manuals: per-task_type ≤ 5). Slightly less uniform, but off the hot path.
- File-based staging can desync (a stale staged file from a crashed run); needs a
  TTL/cleanup sweep.

## Alternatives considered

- **Pure prompt protocol** (engram-style "MANDATORY before done") — rejected as
  the sole mechanism: best-effort, model forgets, abnormal exits escape. Kept as
  the *composition* half, paired with hook enforcement.
- **A new `auto_session_summary` kind** — rejected. Breaks the frozen 4-kind
  enum and ripples into schema, FTS, dedupe, and tier rules for no behavioral
  gain the `origin` column does not already provide.
- **Model-assigned importance score (1–5)** — rejected. Predicted value is
  subjective, drifts run-to-run, costs tokens, and is gameable; measured Expand
  signal is strictly better. A mechanical change-counter prior covers cold start.
- **Background ticker purging on DB size** — rejected. Violates ADR 0010 and
  contends with single-writer SQLite. Replaced by inline save-time eviction.
- **Staging as a DB draft row, or repeated `force`-save as draft** — rejected.
  A DB draft reintroduces the per-turn cold-spawn tax; `force`-as-draft abuses a
  flag defined for HITL correction and writes full save/scrub/dedupe per turn.

## Open items

- M = 30 / K = 5 are locked defaults; re-tune against the T1.6 storage eval.
- Orphaned staged files are recovered by the session-start **recovery flush**
  (decision point 2); a 7-day TTL backstop reaps any file too corrupt/incomplete
  to flush. Locked default; revisit if litter shows up in practice.
- The relevance **floor** for the `UserPromptSubmit` pull (decision point 8) — a
  starting value, to tune against the T1.2 recall eval so it injects real matches
  without weak-hit noise.
- N = 3 (intake + checkpoint threshold) and the Stop-hook block-once semantics
  are locked defaults; tune N against false-positive auto-summaries in practice.
