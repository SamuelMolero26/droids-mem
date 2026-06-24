# 0010 — No automatic retention for knowledge kinds

**Status**: Accepted
**Date**: 2026-06-11

## Context

Only `session_summary` has retention (rolling 5 per task_type). The other three kinds — `error_resolution`, `task_pattern`, `user_rule` — grow without bound, so the PRD §3.2 25 MB storage target and the ~10k-row tuning ceiling on the Jaccard dedupe threshold (save.go) are aspirations with zero enforcement. Growth math says the runway is long (realistic memory ≈ 1–2 KB; heavy use ≈ 7+ years to 25 MB), but noise can accumulate faster locally — e.g. dozens of near-identical-but-under-0.85 error_resolutions from a flaky CI failure.

## Decision

The system never deletes knowledge-kind memories on its own. Memories are distilled user knowledge; recency and age are poor proxies for value (an old rare-error fix may be the most valuable row), so any automatic eviction policy silently destroys data. Instead:

1. **Observability** — `doctor` gains two warnings:
   - DB file > 20 MB (80% of the 25 MB target)
   - total rows > 8,000 (80% of the 10k Jaccard tuning ceiling), message pointing at `prune --suggest-dupes`
2. **Manual prune** — a `prune` command with explicit filters (`--kind`, `--task-type`, `--older-than`), `--dry-run` by default. Deletion only ever happens on explicit human request.
3. **Dupe-cluster suggestion** — `prune --suggest-dupes [--threshold 0.6]` reuses the save-time dedupe mechanism (FTS5 BM25 top-20 candidates → Go Jaccard verification) offline at a relaxed threshold to surface consolidation candidates. Save-time stays strict (0.85: a false positive silently loses a save); prune-time goes loose (0.6: a false positive costs a human a glance).

The `session_summary` rolling-5 prune is unchanged — summaries are explicitly ephemeral state, not knowledge.

**Superseded by ADR-0018 for one narrow case.** This ADR's "never deletes on its own" rule has one carve-out: write-time **supersession** (ADR-0018). When an agent saves a Memory with `supersedes: <id>`, it is *asserting* the named row is replaced — that is an explicit intent at the source, not a recency/age heuristic, so it does not contradict this ADR's actual thesis (machine eviction by poor value proxies is what's banned). The distinction this ADR draws — *the agent knows; recency doesn't* — is exactly why supersession is allowed and auto-TTL is not.

## Mechanics (suggest-dupes)

- Greedy consumed-set clustering: iterate rows in `created_at` order (deterministic, reproducible runs); each unconsumed row seeds a cluster via one FTS5 query + Jaccard filter; all members marked consumed (`map[string]struct{}` keyed by ULID). Denser duplicates → fewer queries.
- One prepared FTS5 statement, whole walk inside a single read-only transaction — skips per-row parse/compile and lock churn, and yields a consistent snapshot (clusters cannot be torn by concurrent saves).
- Known limitation: greedy clustering is order-dependent — clusters are stars around seeds, not cliques. Acceptable for human-reviewed suggestions; one more reason output is suggestions-only, never auto-delete.

## Alternatives considered

- **Auto rolling caps per kind per task_type** (mirror session_summary) — rejected: picking which 50 error_resolutions survive by recency destroys high-value old rows.
- **Age TTL** — rejected: age anti-correlates with value for `user_rule` (oldest rules are the most established).

Both remain possible later as opt-in flags on `prune` (never as defaults) if observed growth demands it.
