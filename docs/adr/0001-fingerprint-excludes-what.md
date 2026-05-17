# 0001 — Fingerprint excludes `what`

**Status**: Accepted
**Date**: 2026-05-17

## Context

The `Fingerprint` (Layer 1 dedupe key) is a deterministic SHA-256 over normalized text. Composition affects what counts as an exact duplicate: any field included in the hash must match exactly for the second save to be skipped.

An audit recommended including the `what` field in fingerprint composition so that two memories with different problem narratives but identical fix would each be stored separately. We rejected that recommendation.

## Decision

Fingerprint composition remains `task_type + kind + title + learned`. The `what` field is intentionally excluded.

```go
fp = sha256(normalize(task_type + kind + title + learned))
```

## Consequences

**Accepted downsides**

- Two genuinely distinct problems with the same fix collapse into one stored memory if `title` and `learned` are also identical. The first save's `what` is retained; subsequent `what` text is dropped on the floor (caller receives `status: "skipped", reason: "duplicate"`).
- An agent that refines the `what` framing on retry must pass `force=true` to overwrite.
- Debug traceability is reduced — the store does not retain a history of every problem framing that led to a given lesson.

**Why we accept them**

- The `learned` field is the lesson worth retaining. The `what` field is the trigger — interchangeable framing of the same situation.
- Including `what` in the fingerprint would bloat the store with rephrased duplicates of the same lesson. Two agents hitting the same bug rarely describe it identically; we would store the same fix twice for no agent benefit.
- Layer 2 (BM25 + Jaccard cosine over full text including `what`) catches genuinely different lessons whose `learned` text happens to overlap. So when `what` differs enough to indicate a different situation, Layer 2 surfaces both memories anyway.
- The `what` field on the surviving record remains FTS-searchable. It is not erased from search, only from identity.
- Aligns with M0 §6 which already accepted this tradeoff implicitly.

## Alternatives considered

- **Include `what` in fingerprint** — rejected for store-bloat reasons above.
- **Per-kind configurable fingerprint** (e.g. `error_resolution` includes `what`, `session_summary` does not) — rejected as overkill for V1; introduces inconsistency that complicates agent reasoning.
