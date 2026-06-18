# 0007 — PII scrub pipeline

**Status**: Accepted (v1.0)
**Date**: 2026-06-08
**Last Updated**: 2026-06-08
**Depends on**: None for v1.0 (ADR 0005 + 0006 deferred to v1.1 / v1.2)

> **v1.0 amendment (2026-06-08):** This ADR ships in v1.0 ahead of workspace model (ADR-0005) and JSONL sync (ADR-0006). Implications:
> - **No workspace.yml tuning in v1.0.** Pattern set + order hardcoded. `extra_patterns` / `disabled_patterns` / `enabled_optional_patterns` / `scrub_pattern_version` config block deferred to v1.1 with workspaces.
> - **Single-DB scope.** No per-workspace scrub stats. `doctor --scrub-stats` aggregates over the single user DB.
> - **Locked v1.0 decisions** (see [v1.0 implementation plan](../v1.0-implementation-plan.md)):
>   - Redaction tokens: bracketed per-category (`[EMAIL]`, `[AWS_KEY]`, …).
>   - Pattern order: pem_key → jwt → aws_key → github_token → stripe_key → slack_token → anthropic_key → openai_key → ssn → credit_card → phone → private_ipv4 → email.
>   - Single-pass merge with longer-wins / earlier-declaration tie-break.
>   - Tags strict-reject on scrub match (`tag_contains_secret`, `retryable:true`).
>   - Empty-after-scrub on `learned` rejects (`scrub_emptied_learned`).
>   - `scrub_counts` JSON column on memories for doctor aggregation (refactor target post-v1.1).

## Context

V1 ships `internal/store/scrub.go` as a pass-through stub. `scrubPII()` is wired into the save pipeline via `validate()` so call sites are stable, but no patterns are applied. Every `mem_save` writes `title`, `what`, `learned`, and `tags` verbatim to disk.

This is acceptable for a single-user, single-machine, never-committed memory store. It is **not** acceptable once ADR 0006 lands: `scope=shared` memories from `project` workspaces export to `memories.jsonl` and get committed to git. Any API key, JWT, email, or customer identifier the agent learned about lands in repo history, propagates to coworker clones, and survives `git rm` (history rewrite required to fully remove).

This is the single largest pre-release blocker flagged in `future.todo`. Without it:

- Public OSS release is unsafe — first agent that learns a secret leaks it to the repo.
- Workflow workspaces running 24/7 accumulate weeks of unscrubbed identifiers in their local DB. Any backup or `droids-mem export` leaks the lot.
- TUI inspection (ADR 0005) renders raw secrets to the terminal.

The scrub layer must run before any write hits disk, must be deterministic, must be fast (sub-millisecond on typical memory bodies), must be configurable so workspaces with different sensitivity profiles can tune it, and must be testable against a fixture corpus to keep false-positive / false-negative rates in check.

Embedding-based or LLM-based PII detection is rejected up front: violates the determinism wedge, adds dependencies, costs latency and money, and produces non-reproducible scrubs across machines (a coworker's clone would re-scrub differently). Regex patterns plus structural checks (Luhn for credit cards) are sufficient for the threat model and fast.

## Decision

Implement `scrubPII()` as a deterministic, regex-based pipeline applied to all free-text fields in the save path. Scrub runs **before** fingerprint computation and **before** dedupe so two memories carrying the same secret-bearing text scrub to identical canonical form and dedupe correctly.

### Pipeline location

`scrubPII()` is called from `validate()` in `internal/store/save.go`, after field-presence checks and trim, before `normalizeForFingerprint()`. Pipeline order is fixed:

```
1. validate required fields, types, enum membership
2. trim each field
3. scrubPII(field) for each of title, what, learned, tags
4. normalizeForFingerprint(title + learned)
5. fingerprint = sha256(normalized + task_type + kind)
6. dedupe layer 1 + 2
7. insert
```

Scrub mutates `title`, `what`, `learned`, `tags` in place on the in-memory `Memory` struct. The redacted text is what gets fingerprinted, dedupe-checked, FTS-indexed, and persisted. There is no "original" copy retained anywhere.

### Pattern set (V1)

Patterns are compiled once at package init and reused. Each pattern has: regex, replacement token, optional structural check, and a name used in scrub logs.

| Name | Pattern (Go re2) | Replacement | Structural check |
|------|------------------|-------------|------------------|
| `email` | `\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b` | `<email>` | none |
| `phone_e164` | `\+?\d{1,3}[\s\-.]?\(?\d{1,4}\)?[\s\-.]?\d{1,4}[\s\-.]?\d{1,9}\b` | `<phone>` | length 7-15 digits after strip |
| `credit_card` | `\b(?:\d[ \-]?){13,19}\b` | `<cc>` | Luhn checksum must pass |
| `ssn_us` | `\b\d{3}-\d{2}-\d{4}\b` | `<ssn>` | none |
| `aws_access_key` | `\b(AKIA|ASIA)[0-9A-Z]{16}\b` | `<aws-key>` | none |
| `aws_secret_key` | `\b[A-Za-z0-9/+=]{40}\b` (context-gated, see below) | `<aws-secret>` | requires `aws_secret` or `aws_access_key` within 100 chars of match |
| `github_token` | `\bghp_[A-Za-z0-9]{36}\b` | `<github-token>` | none |
| `github_token_other` | `\b(ghu_|ghs_|gho_|ghr_)[A-Za-z0-9]{36}\b` | `<github-token>` | none |
| `stripe_secret` | `\bsk_(test|live)_[A-Za-z0-9]{24,}\b` | `<stripe-key>` | none |
| `stripe_publishable` | `\bpk_(test|live)_[A-Za-z0-9]{24,}\b` | `<stripe-key>` | none |
| `slack_token` | `\bxox[abprs]-[A-Za-z0-9\-]{10,}\b` | `<slack-token>` | none |
| `openai_key` | `\bsk-[A-Za-z0-9]{32,}\b` | `<openai-key>` | none |
| `anthropic_key` | `\bsk-ant-[A-Za-z0-9\-_]{20,}\b` | `<anthropic-key>` | none |
| `jwt` | `\beyJ[A-Za-z0-9_\-]+\.eyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\b` | `<jwt>` | three base64-url segments separated by `.` |
| `private_key_pem` | `-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----` | `<private-key>` | none |
| `ipv4_private` | `\b(10\.\d{1,3}\.\d{1,3}\.\d{1,3}|192\.168\.\d{1,3}\.\d{1,3}|172\.(1[6-9]|2\d|3[01])\.\d{1,3}\.\d{1,3})\b` | `<private-ip>` | each octet 0-255 |
| `ipv4_public` | `\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b` | `<ip>` | each octet 0-255, **disabled by default** (high false-positive rate on version strings) |
| `uuid` | `\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b` | unchanged | **disabled by default** (UUIDs are usually not secrets; opt-in for compliance workloads) |

Patterns are applied in deterministic order (as listed). Earlier patterns win on overlap: an AWS access key matched as `aws_access_key` will not be re-matched as `phone_e164`.

### Determinism guarantees

- Pattern set + order is fixed per `schema_version`. Two clones with the same droids-mem version produce byte-identical scrubs.
- No randomness, no time-dependent behavior, no environment-dependent behavior.
- Replacement tokens are stable strings (`<email>`, not `<email-1>` or `<email-abc>`). Multiple emails in one field collapse to one replacement per occurrence, not per unique value. This is intentional: leaking "the same email appears twice" is itself a signal worth suppressing.

### Workspace configuration

Each workspace's `workspace.yml` may include a `scrub` block:

```yaml
scrub:
  enabled: true                  # default true; false disables all scrubbing
  extra_patterns:                # workspace-specific regexes
    - name: customer_id
      regex: 'CUST-\d{8}'
      replacement: '<customer-id>'
  disabled_patterns:             # opt-out of default patterns by name
    - uuid
  enabled_optional_patterns:     # opt-in to default-disabled patterns
    - ipv4_public
    - uuid
```

- `enabled: false` is allowed but logged loudly on workspace open. Useful for offline dev workspaces with no risk surface. Never use on workspaces with `sync.mode: git-jsonl`.
- `extra_patterns` are compiled at workspace load. Compilation errors fail workspace open with a clear message; no silent skip.
- Pattern names must be unique within a workspace (default ∪ extra). Duplicates fail load.
- Default patterns are the source of truth; workspace-level changes are additive or subtractive, never overriding the regex of a default pattern. To replace a default, disable it and add an extra with a different name.

### Scrub events and observability

Each scrub call returns a `ScrubReport` consumed by the save layer:

```go
type ScrubReport struct {
    Patterns map[string]int  // pattern name → match count
    TotalRedactions int
}
```

`save.go` does not log this directly. `mcpserver/tools.go` includes the report in the `mem_save` response when redactions occurred:

```json
{
  "status": "saved",
  "id": "mem_01J...",
  "session_id": "sess_01J...",
  "scrub": {
    "redactions": 3,
    "patterns": {"email": 2, "aws_access_key": 1}
  }
}
```

This gives the agent (and the user reviewing logs) immediate signal that something was redacted, without exposing what was redacted.

`droids-mem doctor` aggregates redaction counts across the workspace: per-pattern totals, redactions-per-day trend, top-redacting `task_type`. Useful for tuning workspace patterns and spotting agents that should not be learning secrets at all.

### Testing strategy

Fixture corpus at `internal/store/testdata/scrub/`:

- `inputs/` — one file per scenario, raw text containing real-shape secrets (generated, not actual)
- `expected/` — corresponding scrubbed output
- `negatives/` — text that **must not** trigger scrubs (version strings, hex hashes, ULIDs, base64 image fragments) with expected output equal to input

Tests:

1. Per-pattern unit tests (`scrub_test.go`) — each pattern hits its positives, misses its negatives, with table-driven cases.
2. Pipeline integration test — full corpus through `scrubPII()`, compared byte-for-byte to `expected/`.
3. Determinism test — scrub the corpus 100× in random pattern-order builds (via build tag); output must be identical.
4. False-positive budget test — corpus of 1000 lines of synthesized "normal" prose + code snippets; redaction count must stay below a documented threshold per category (e.g., `phone_e164` < 3 false positives per 1000 lines).
5. Performance test — benchmark `scrubPII()` on 10 KB body; must complete < 500 µs on M-series Mac. Regression budget tracked.

### CLI surface

```
droids-mem scrub --check <file>          # run scrub against arbitrary text, print report
droids-mem scrub --test                  # run fixture corpus tests, print pass/fail summary
droids-mem doctor --scrub-stats          # workspace-level redaction stats
```

No CLI command exists to "unredact" or recover original text. The scrub is one-way and lossy by design.

### Migration

Existing V1 databases were written with the pass-through stub. On droids-mem upgrade:

- `droids-mem migrate --rescrub-workspace <name>` walks every row, runs current scrub patterns, rewrites in-place if anything matches. Reports per-row diffs to stderr.
- This is opt-in and irreversible. Documented in upgrade notes; never run automatically on workspace open.

## Consequences

**Accepted**

- Public OSS release is safe: no plausible secret pattern survives the save path without being redacted (within the documented coverage).
- `project` workspaces can sync via git without exposing secrets — `memories.jsonl` contains `<email>`, `<aws-key>`, etc., never raw values.
- Determinism is preserved across machines: two clones running the same droids-mem version produce identical fingerprints for the same scrubbed text. Dedupe works correctly across coworker boundaries.
- TUI rendering is safe: redacted text reaches the terminal, raw secrets do not.
- The scrub layer is the same pure-Go, no-deps approach as the rest of droids-mem. No new runtime dependencies.

**Tradeoffs**

- False positives will happen. A `task_pattern` describing "use `pk_test_...` for testing Stripe" will have the example value redacted. Mitigated by `extra_patterns` / `disabled_patterns` per workspace, but not eliminated. Documented loudly.
- False negatives will also happen. Novel API key formats, custom internal token schemes, and short-form identifiers will pass through. Mitigated by per-workspace `extra_patterns` and by `doctor --scrub-stats` surfacing what isn't being caught.
- Once scrubbed, original text is unrecoverable from droids-mem. A user who wants to embed a real example secret must do so outside droids-mem (e.g., a `.env.example` file) and reference it by name in the memory.
- Adding the scrub step costs ~100-500 µs per save. Negligible relative to MCP roundtrip and LLM cost; benchmarked in CI.
- Pattern set is a moving target. New secret formats appear (Anthropic keys did, GitHub fine-grained PATs did). Pattern updates require a droids-mem version bump and a rescrub migration to apply to historical rows.
- `schema_version` in JSONL (ADR 0006) must include the scrub-pattern-set version so two clones with different droids-mem versions can detect they have inconsistent scrub coverage and prompt the user to migrate.

## Alternatives considered

- **No scrubbing, document risk and leave it to the agent** — rejected. Pushing PII responsibility onto the agent / user means the first mistake is also the last (secret in git history). Defense in depth requires scrubbing at the store layer.
- **Allowlist-only fields ("only store fields matching this schema")** — rejected. Memory bodies are free text by design; an allowlist can't model "what" and "learned" without breaking the product.
- **LLM-based PII detection** — rejected. Non-deterministic, slow, expensive, breaks the determinism wedge, requires API key (irony noted), and produces different scrubs across clones depending on model version.
- **Microsoft Presidio or external PII library** — rejected for V1. Heavyweight dependency, NLP models, Python runtime. Reconsider only if false-positive/negative rates with regex prove unacceptable.
- **Hash secrets in place rather than redacting** — rejected. Hashed secrets are still useful for confirming "the same secret appears here" attacks; provides no privacy benefit, complicates UX.
- **Encrypted-at-rest with scrubbed view on export** — considered. Holds raw text in the DB encrypted, scrubs only at export to JSONL. Rejected for V1: adds key-management surface (where does the encryption key live? how is it shared across coworkers?) and TUI inspection becomes complicated. Revisit if regulated-industry users demand it.
- **Per-call scrub override flag (`--no-scrub`)** — rejected. Anything that lets a single call bypass scrub becomes the default "I'll fix it later" escape hatch. Workspace-level config is the right granularity.

## Open questions (to resolve before implementation)

1. **`tags` field handling.** Tags are space-delimited tokens. Email-shaped tokens in tags scrub to `<email>` and break FTS tokenization in surprising ways. Proposal: scrub tags with a more restrictive set (no `phone_e164`, no `credit_card`, no `ipv4_*`) since tags should never carry those by convention.
2. **Scrub report exposure on CLI saves.** MCP returns it in JSON; CLI `droids-mem save` also returns JSON, so consistent. Confirm CLI users want the field always present or only on redaction.
3. **Workspace-level scrub disable lock.** Should `enabled: false` be allowed at all? Or only via a build tag for debugging? Proposal: allowed but logged at WARN every save, plus a startup-time loud notice. Disabling is a foot-gun but legitimate for offline dev.
4. **JSONL schema_version bump policy.** Scrub-pattern changes ship in droids-mem versions; do they bump JSONL `schema_version` or a separate `scrub_pattern_version` field? Proposal: separate `scrub_pattern_version` so JSONL stays compatible across scrub updates and rescrub migration can detect version drift.
5. **Coworker clone with weaker scrub.** Two coworkers on different droids-mem versions: one scrubs aggressively, one does not. The JSONL committed by the older version may contain raw secrets even after the newer version's `sync`. Proposal: on `droids-mem sync --import`, run current scrub patterns over every imported row, write back if anything matches, and stage the JSONL re-export. Solves it.
6. **Telemetry of pattern hit rates.** `doctor --scrub-stats` is local-only. Do we want an opt-in aggregate metric to inform future pattern updates (e.g., "Anthropic key pattern matches 80% of memories in this workspace — agents are leaking constantly")? Proposal: local-only for V1, decide on opt-in aggregation later.
