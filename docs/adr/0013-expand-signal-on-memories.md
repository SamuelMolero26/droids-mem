# Expand signal tracked as columns on memories

An agent-facing `get` (CLI `get`, MCP `mem_get`) increments `expand_count` and sets `last_expanded_at` (unix **seconds**, matching `created_at`/`updated_at`) on the target Memory row. Two columns on `memories` rather than a separate `access_events` table — consistent with the `scrub_counts` precedent and sufficient for the goal of informing future browse-tier sizing decisions.

The alternative (dedicated `access_events` table with per-call rows) would give temporal resolution and per-source attribution but couples a stats schema to a local-first single-user DB that has no consumers for fine-grained access logs yet. The two-column approach is accepted as "expand signal, not browse-chain signal" — counts are total-lifetime, not time-windowed; `last_expanded_at` distinguishes stale-hot from recent-hot. Surfaced via `doctor --expand-stats`; a full `access_events` table is deferred to v1.2 when workspace-scoped rollups become necessary.

## What counts, and what does not

The signal is meant to capture *agent consumption* (which browse-tier IDs an agent expands), so the counting path is deliberately narrow:

- **Counts**: `store.Get` — the agent-facing fetch behind CLI `get` and MCP `mem_get`.
- **Does NOT count**: operator/TUI reads go through `store.GetRow`, a pure fetch with no side effect. An interactive inspector paging through memories would otherwise be the single largest polluter of the very signal the feature exists to capture.

## Seam shape

`Get` is a composition, not a fused method:

```
GetRow(ctx, id)        // pure fetch — no side effect (TUI/operator)
Get(ctx, id)           // = GetRow + recordExpansion (agent-facing)
recordExpansion(ctx, id)  // best-effort telemetry, lives in expand.go
```

`recordExpansion` is **best-effort**: a single autocommit `UPDATE … WHERE id=?` on the request context, no wrapping transaction. Every failure is swallowed (logged, never propagated) — a counter that cannot write must not fail the read it measures, and a lost increment under write-lock contention or request cancellation is acceptable for approximate telemetry. It fires only after `GetRow` returns a non-nil Memory.

The recorder is a concrete unexported method, not an interface — one implementation today is a hypothetical seam, not a real one. The batched/async/sidecar swap anticipated for write-on-read pressure (see below) is a mechanical extraction against this call site when a second adapter actually exists; it is not pre-built.

## Force-save survival

Force-save is an in-place `UPDATE memories SET title=…, WHERE id=?` (`forceUpdateConn`) that does not list `expand_count`/`last_expanded_at`, so both survive HITL correction automatically — no carry-forward code, just a regression test pinning the invariant against a future rewrite of the force path.

## FTS trigger interaction

The increment `UPDATE` must not touch the FTS index. The `memories_au` trigger is scoped to `AFTER UPDATE OF title, what, learned, tags` (migration v1→v2) so a metadata-only update does not delete+reinsert FTS content. Without this scoping every counted `get` would re-index four FTS columns — orders of magnitude worse than the bare counter write.

## Performance posture

With `db.SetMaxOpenConns(1)` the process already serializes all reads and writes through one connection, so adding a write to `get` introduces no *new* serialization — it does add WAL-commit volume on the post-browse hot path. Accepted for v1.x. If concurrent-MCP pressure ever surfaces, the escape hatch is to batch increments in memory and flush periodically, or move counts to an async-written sidecar — deferred, not pre-optimized.
