# 0011 — User_rule overflow surfaces as browse-tier stubs

**Status**: Accepted
**Date**: 2026-06-11

## Context

ADR-0002 promised the always tier returns ALL `user_rule` rows ("user preferences must always apply — no truncation") and flagged unbounded growth as an open tradeoff. Locked decision #20 later capped the always tier at the 5 newest rules to protect the bundle payload budget — but the cap drops older rules *silently*. An agent that never sees rule #6 has no signal it exists and may violate it; "reachable via `mem_search`" only helps an agent that knows to search.

## Decision

The always tier keeps the 5 newest full-body rules (decision #20 unchanged). Rules beyond the cap are returned in the same bundle as browse-tier stubs — `{id, kind, title, tier: "browse"}`, no snippet — and the response gains a `user_rules_total` count. The agent sees every rule's title, triages relevance itself, and fetches full bodies via `mem_get` — the existing lazy-tier escape hatch.

No new tier name: the glossary already defines browse tier as titles for shallow scanning plus lazy expansion, which is exactly what stubs are. Cost is ~200 bytes per overflow rule (title cap), bounded and budget-compatible.

## Alternatives considered

- **Count only** (`user_rules_total` with no stubs) — rejected: tells the agent rules are hidden but not *which*, forcing a blind search it will usually skip.
- **Uncap the always tier** — rejected: re-opens the unbounded payload problem decision #20 closed (each rule body ≤ 4 KB).

## Consequences

- Partially supersedes the always-tier user_rule clause of ADR-0002 (as amended by decision #20).
- Response shape change: browse tier may now contain `user_rule` items and the bundle gains `user_rules_total`. Only V1-internal callers exist.
