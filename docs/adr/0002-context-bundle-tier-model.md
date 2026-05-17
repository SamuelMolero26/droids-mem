# 0002 — Context bundle tier model

**Status**: Accepted
**Date**: 2026-05-17

## Context

The original `context` command returned a fixed slot allocation (1 session_summary + 3 error_resolution + 2 user_rule + 2 task_pattern = 8 memories) capped by a numeric `--limit` flag. The implementation truncated the assembled bundle post-hoc by append order, so passing `--limit 5` silently dropped `task_pattern` and possibly `user_rule` memories — non-deterministic and opaque to the caller.

Beyond the truncation bug, the numeric-limit knob itself was a poor fit for agent consumers: agents reason about intent ("I want orientation" vs. "I want depth on this query"), not about cardinality.

## Decision

Replace the slot+limit model with a two-tier bundle and lazy-tier escape hatch.

**Always-tier** (returned in full body):
- Latest 1 `session_summary` for the `task_type`
- ALL `user_rule` rows for the `task_type` (user preferences must always apply — no truncation)

**Browse-tier** (returned as `{id, kind, title, snippet}` where `snippet` is the first 120 characters of `what`):
- Top 10 `error_resolution` by BM25 against `--query` (or tokenized `task_type` if no query)
- Top 10 `task_pattern` by BM25 against `--query` (or tokenized `task_type` if no query)

**Lazy-tier** (not returned; agent-initiated):
- Agent calls `droids-mem get --id mem_X` for full body of any browse-tier item it wants to read.
- Agent calls `droids-mem search --query ...` for arbitrary on-demand lookups during the run.

The `--limit` flag is removed from `context`. Response shape gains a `tier` field per memory (`"always"` or `"browse"`) so callers know whether to call `get` for depth.

## Consequences

**Accepted**

- Agents always get the small, expensive-to-recompute orienting state (latest summary + all rules) in full. No risk of `user_rule` being silently dropped at small limits.
- Agents get a wide, cheap browse list (~20 titles + 1-liners ≈ ~600 tokens) to scan and selectively expand.
- Total token cost per `context` call is predictable and bounded by the size of always-tier + ~20 browse entries.
- Removes the unanswerable agent question "what number should I pass to `--limit`?"

**Tradeoffs**

- Bundle shape is now fixed — callers wanting a different mix must call `search`/`list` directly.
- Always-tier returning ALL `user_rule` rows could grow unbounded if rules accumulate without compaction. Mitigation: future auto-compaction of superseded rules (tracked in Future.md).
- New `tier` field on response is a breaking change for any pre-existing caller of `context`. No external callers exist yet in V1.

## Alternatives considered

- **Proportional scale of original slots to `--limit`** — fixes truncation determinism but keeps the bad numeric-knob UX.
- **Per-kind flags (`--max-errors`, `--max-rules`)** — pushes burden onto agents to guess good numbers.
- **`--mode orient|deep|refresh` presets** — better UX than numeric limit but premature. Deferred to Future.md once we observe agent behavior on the tier model.
- **Single ranked list across all kinds** — errors dominate due to length-biased BM25; user_rules crowded out.
