# 0018 — Write-time supersession (hard-delete on `supersedes`)

**Status**: Accepted
**Date**: 2026-06-21

## Context

`user_rule` memories evolve. When a user changes a stable preference ("use tabs"
→ "use spaces"), the old rule does not vanish — both land in the always-tier
(newest 5 `user_rule` rows, full body, returned on every `mem_context`). The
agent then reads two contradicting rules and has no way to know which is current.
This is an active correctness problem, not a storage one: unlike
`error_resolution`/`task_pattern` (browse-tier, ranked, behind `mem_get`), a stale
`user_rule` is shoved into the agent's face every run.

Detecting supersession *after the fact* is semantic — "use tabs" and "use spaces"
have high token overlap and opposite meaning, so BM25/Jaccard (the only retrieval
machinery available, ADR-0001) cannot distinguish *supersede* from *agree*.
Embedding/LLM contradiction detection was rejected (local-first, pure-Go,
no-CGO constraints — same reasoning that killed embedding-based search).

The one entity that *knows* a new rule replaces an old one is the agent writing
it: it has the run context and just made the decision. Capture that intent at the
source instead of reconstructing it later.

## Decision

`mem_save` gains an optional `supersedes: <id>` parameter (MCP arg + `SaveRequest`
field). When set, the named target is **hard-deleted in the same `BEGIN IMMEDIATE`
transaction as the insert**, via a single scope-bound `DELETE`.

1. **Hard delete, not soft-mark.** No `superseded_by` column, no migration, no
   schema change. `supersedes` is a transient request parameter that triggers a
   `DELETE`; it is never persisted. The corpus stays clean — a superseded rule is
   *gone*, not demoted. (Modeled on how a brain forgets what no longer applies
   rather than carrying dead weight.) Cost of a wrong guess is accepted as the
   price of a clean working set.
2. **Any Kind; target must match Kind.** Supersession is general (an
   `error_resolution` can be obsoleted too), but the new Memory and its target
   must share a Kind — you don't supersede a rule with a bugfix.
3. **Validation is the `WHERE` clause, not Go branches.** The delete is
   `DELETE FROM memories WHERE id=? AND kind=? AND task_type=? AND scope=?` bound
   to the *new* Memory's kind/task_type/scope. A target that mismatches any of
   those — or doesn't exist — simply matches 0 rows: no delete, echo
   `superseded: null`. Cross-boundary deletion is therefore *impossible by
   construction* (the `WHERE` can't reach a row in another scope/task_type/kind),
   with no fetch-then-validate step and no `ValidationError` branches. Mismatch and
   missing collapse to the same benign, observable outcome. (Simplified from an
   earlier strict-reject design in a ponytail review — see Alternatives.)
4. **Single target.** One id, scalar. One-to-many merge is a different, fuzzier
   operation and is not built (YAGNI).
5. **Exempt the target from dedupe.** A replacement resembles what it replaces,
   so the target is the row most likely to trip the ≥0.85 near-duplicate gate and
   get the new save skipped — self-defeating. The named target is excluded from
   the near-duplicate candidate scan (`AND m.id != ?`); dedupe still runs against
   every other row.
6. **Fires only on a successful insert.** The `DELETE` runs *after* the insert in
   the same txn, so if the new save is deduped-away by some *other* row, no delete
   happens — never destroy the old rule when the replacement didn't land. Insert +
   delete commit or roll back together. The FTS delete trigger keeps the index
   synced for free.
7. **Echo-only, no persisted trace.** `SaveResponse` reports `superseded: <id>`
   (set only when `RowsAffected > 0`, else `null`) so the acting agent and logs
   can see what was deleted. Nothing is stored — the link would dangle against a
   deleted row.

`--dry-run` and `force` need no special handling: dry-run already runs the full
pipeline then `ROLLBACK`s, so the delete is previewed via the echo and rolled back
for free; `force` (overwrite a fingerprint twin) is orthogonal to `supersedes`
(replace a named different row) and the two compose without precedence rules.

The CLI `save` command does **not** get a `--supersedes` flag. Per ADR-0004 only
the Root agent writes (via MCP); CLI `save` is an operator/manual path where
hand-typing a ULID target is not a real workflow. Add the flag if an operator use
case actually appears (YAGNI).

## Consequences

- Amends **ADR-0010** ("never deletes knowledge-kind memories on its own"). The
  carve-out is consistent with 0010's *actual* thesis: 0010 bans eviction by poor
  value proxies (recency, age). Supersession is an explicit agent assertion, not a
  heuristic — the very distinction 0010 draws ("the agent knows; recency doesn't")
  is what licenses it.
- Collides with **CONTEXT.md**'s framing of `Prune` as the *only* deletion path
  (which lists `compaction` under _Avoid_). CONTEXT.md updated: `Prune` notes the
  Supersede exception, and a `Supersede` term is added.
- A wrong supersede (agent over-generalizes, deletes a still-true narrower rule) is
  silent and permanent. Accepted deliberately — the clean-corpus value won over
  recoverability. The echo is the only mitigation.
- A *mismatched* target (wrong scope/task_type/kind) is a silent no-op rather than
  a loud error — the `WHERE` clause just matches nothing. The acting agent still
  observes `superseded: null`, so a wrong target is visible at call time, just not
  raised as a `ValidationError`. Accepted as the price of dropping the
  fetch-then-validate branches (this is a single-user local tool; the same agent
  writes the id it read).

## Alternatives considered

- **Soft-mark (`superseded_by` column, demote out of always-tier, keep row
  queryable)** — rejected: user preferred a clean corpus over recoverability
  ("like a brain, you forget what is not applicable and move on"). Soft-mark also
  costs a column, a migration, an always-tier filter clause, and leaves dead rows
  in the FTS index (the exact BM25-dilution noise we otherwise avoid).
- **Demote-now, prune-later** (soft-mark immediately, sweep via human `prune`) —
  rejected for the same reason; kept ADR-0010 more intact but didn't deliver the
  immediate clean corpus the user wanted.
- **Automatic semantic contradiction detection** — rejected: requires
  embeddings/LLM, off the table on local-first/no-CGO grounds.
- **Read-time human-confirmed supersession** (a `prune --suggest`-style cluster) —
  rejected: throws away the write-time intent the agent already has, and leaves the
  always-tier polluted until a human intervenes.
- **Strict-reject validation** (fetch the target, compare kind/task_type/scope in
  Go, raise `ValidationError` on mismatch; only a missing target is benign) — the
  original grill decision, rejected on ponytail review. The scope-bound `DELETE`
  achieves the same safety (no cross-boundary delete) by construction, in one
  statement, dropping a fetch + three validation branches + four tests. The only
  loss is loud-error-on-mismatch, downgraded to an observable silent no-op —
  acceptable for a single-user local tool.
