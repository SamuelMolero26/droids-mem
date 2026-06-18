# 0009 — Store owns ValidationError serialization; transports extend via embedding

**Status**: Accepted
**Date**: 2026-06-10

## Context

`ValidationError` was being hand-mapped to JSON independently in each transport adapter. The CLI adapter hardcoded `code: "validation_failed"` and `retryable: false` regardless of the actual error, and silently dropped `Code`, `Retryable`, `OffendingTags`, `MatchedPatterns`, and `Scrub`. An agent using Subprocess transport got `"validation_failed"` where MCP returned `"tag_contains_secret"` — two transports, diverging error contracts, bugs by omission. Any new field on `ValidationError` required updating multiple adapters across two packages.

## Decision

`ValidationError.ToEnvelope()` returns a `ValidationEnvelope` — the canonical serializable error shape owned by the store package. All transport adapters must use it as their base. No adapter may hand-roll a `ValidationError` JSON shape.

**Default code**: when `ValidationError.Code` is empty (generic field-presence failures), `ToEnvelope()` defaults `Code` to `"validation_failed"`. This preserves existing CLI behavior while letting specific rejection codes (`tag_contains_secret`, `scrub_emptied_learned`, etc.) surface through on both transports.

**Transport extensions via struct embedding**: each transport embeds `ValidationEnvelope` and adds transport-specific fields only:

- **MCP** adds `error: "validation_error"` — a type discriminator for agent-side pattern matching.
- **CLI** adds `input: {field: value}` — the offending flag value for operator debugging.

These extras are additive only. The base envelope fields are never overridden at the transport layer.

## Consequences

- Adding a field to `ValidationError` and `ValidationEnvelope` propagates to both transports automatically.
- CLI and MCP now surface identical base error payloads; transport-specific fields are the only divergence.
- New transport adapters (gRPC, webhook, etc.) must call `ToEnvelope()` rather than mapping fields manually — the rule is enforced by convention, not the type system.
- Minor MCP wire change: generic validation errors now include `code: "validation_failed"` where previously the field was absent (omitempty on empty Code). Additive only.
