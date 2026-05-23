# 0004 — Parent-as-memory-broker pattern for agent consumers

**Status**: Accepted
**Date**: 2026-05-19

## Context

`droids-agents` (the V1 multi-agent BI runtime built on agentspan) is the first non-trivial consumer of the `droids-mem` MCP bridge defined in ADR 0003. Its topology is a Root agent that dispatches to one of four Subteams (research / docs / form / messaging), each of which fans out into multiple Sub-agents running in parallel or in handoff strategies.

Two consumer-side questions surfaced during planning that have direct implications for `droids-mem`'s data model and dedupe semantics, so they are captured here for posterity even though no `droids-mem` code changes ship as part of this ADR:

1. **Should sub-agents call `mem_save` directly?** Each leaf Sub-agent produces structured output (`CompetitorFinding`, `DocSynthesis`, `FormPlan`, etc.). Naively each could write its own observation row.
2. **Does `droids-mem` need a fifth `kind` (e.g. `observation`) to model intermediate Sub-agent output?** The four V1 kinds — `session_summary`, `task_pattern`, `error_resolution`, `user_rule` — are tuned for *durable, reusable* signal, not transient per-step output.

The decision below is the consumer-side contract; recording it in this repo prevents future drift where a different agent runtime tries to re-open the same questions and re-shape the schema.

## Decision

**The 4-kind enum stays. No `observation` kind is added.** `droids-mem` schema, dedupe, and retention bounds are unchanged.

**Sub-agents do not call `mem_save` directly.** The Root agent of each Execution is the sole `droids-mem` writer for that Execution. Sub-agent results flow back through agentspan's durable execution log — *that* log is the granular per-step store. `droids-mem` stays the cross-Execution semantic store.

**Concretely:**

- Root agent runs `memory_loader` first → calls `mem_context(task_type=…, query=…)` → receives `session_id` + Bundle → threads both through the rest of the run.
- Subteam Sub-agents (`competitor_*`, `extractor`, `synthesizer`, `form_planner`, `form_executor`, `drafter`, `sender`) get NO `droids-mem` tools. They consume sliced Bundle context injected by the Root, do their work, and emit structured Pydantic output.
- Root agent runs `rollup` last → composes a `RollupResult { summary, new_patterns[], new_errors[], new_rules[] }` from Sub-agent outputs + HITL events → fans out into N `mem_save` calls, all sharing the same `session_id`.
- Retention bounds (`session_summary` ≤ 5 per `task_type`) continue to apply unchanged.

**Why no `observation` kind:**

- Sub-agent output is high-volume, low-distillation, and Execution-scoped. It is exactly what agentspan's execution log already stores durably.
- Adding `observation` would force every consumer to re-derive what `droids-mem` is *for*: cross-Execution reusable signal. Today the four kinds each answer a distinct retrieval question (recap last run, reuse a recipe, avoid a known failure, honor a user rule). A fifth dump-everything kind dilutes that intent and bloats FTS5 ranking.
- Dedupe (fingerprint + BM25 pre-save) is tuned for distilled signal. Raw observations would pollute both layers — fingerprints rarely collide across runs, and BM25 noise grows with vocabulary churn.

**Why Root-only writes:**

- One writer per Execution makes dedupe a non-issue at the consumer layer — Root composes the Rollup deterministically from typed Sub-agent outputs, so identical inputs across replay produce identical writes.
- Single writer simplifies cost accounting: the Rollup body includes the aggregate `Cost: $X` line.
- Replay safety: if agentspan replays a paused Execution, only the Root's Rollup step interacts with `droids-mem`. Sub-agents are pure compute against the cached Bundle; their replays do not produce extra `mem_save` traffic.

## Consequences

**Accepted**

- `droids-mem` schema, dedupe rules, retention bounds, and FTS5 index stay exactly as they are at V1. No migration burden induced by `droids-agents`.
- The MCP tool surface (`mem_save`, `mem_search`, `mem_context`, `mem_get`) is sufficient for the broker pattern as-is.
- Sub-agent outputs remain queryable via agentspan's own execution-log UI / API — the right tool for that data, scoped correctly.
- `droids-mem` stays semantically tight: every row is something a *future* Run wants to recall.

**Tradeoffs**

- Future consumers that want every Sub-agent step persisted in `droids-mem` will have to either roll their own broker on top of `mem_save` or maintain a fork. Acceptable — the consumer can still write extra `task_pattern` / `error_resolution` rows from Root if a Sub-agent surfaces something distilled.
- Root-only writes mean a crashed Root before Rollup loses the per-step signal from `droids-mem`'s perspective. agentspan's execution log still holds it; replay re-runs Rollup. Net: no data loss, just a delayed write.
- Dedupe relies on the consumer composing the Rollup from typed outputs (not freeform LLM prose). Consumers that emit Rollups via unconstrained LLM generation may produce drift between runs, weakening fingerprint hit rate. Mitigation lives in the consumer, not `droids-mem`.

## Alternatives considered

- **Add an `observation` kind for raw Sub-agent output** — rejected. Bloats FTS5, dilutes the retrieval contract, and forces every future consumer to filter `kind != observation` for the four existing semantic queries. agentspan's execution log already covers durable per-step persistence.
- **Let every Sub-agent call `mem_save` directly** — rejected. Multiplies write traffic by fan-out factor, makes dedupe contend with concurrent inserts (race window already noted in audit findings), and scatters one Execution's memories across N writes with subtly different fingerprints.
- **Make Root the writer but expose a `mem_save_batch` tool** — viable but not needed at V1. The N writes per Rollup are bounded (≤ 1 summary + 3 patterns + 3 errors + 2 rules = 9 max) and the MCP roundtrip cost is negligible against LLM cost. Revisit if a future consumer produces >50 rows per Rollup.
- **Tie `session_id` ownership to a per-connection session map on the MCP server** — already rejected in ADR 0003 for the same durability reasons; reaffirmed here.
