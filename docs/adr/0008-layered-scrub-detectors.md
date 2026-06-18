# 0008 — Layered scrub detectors with a declarative spec

**Status**: Accepted (pattern version 3)
**Date**: 2026-06-10
**Depends on**: ADR-0007 (PII scrub pipeline) — amends its "hardcoded pattern set" decision; keeps its engine semantics and its rejection of ML-based detection.

## Context

The v/Users/samuel/1.0–v2 scrub engine (ADR-0007) is a single detector class: provider-prefix
regexes (`ghp_…`, `sk-ant-…`, `AIza…`). Three problems surfaced in practice:

1. **Enumeration treadmill.** Every new provider is a Go code change, a
   version bump, hand-edited corpus cases, and a hand-synced README table.
   Pattern v2 (4 new providers) took a full change cycle including a
   GitGuardian remediation fight over the fixtures.
2. **Unknown-vendor false negatives.** A secret with no enumerated prefix —
   `password = x7Kp…`, `Authorization: Bearer …`, the password in
   `postgres://admin:S3cret@host/db` — passed to disk untouched. These are
   the most common secret shapes in dev-lesson prose, and no prefix list can
   ever cover them.
3. **Engine/policy coupling.** Engine, pattern table, and save-path policy all
   lived in `internal/store`, while future.todo commits v1.1 to per-workspace
   scrub config (`extra_patterns`, `disabled_patterns`) that a hardcoded Go
   slice cannot serve.

Threat model is reaffirmed as **secrets-first** (developer credentials leaking
into git-synced JSONL exports, ADR-0006); PII coverage (email, E.164 phone,
private IPv4) remains incidental. NER-grade PII detection stays rejected for
the same reasons ADR-0007 rejected ML detection: determinism, dependencies,
false-positive rate (ssn/credit_card were dropped pre-v1.0 on FP grounds).

## Decision

### 1. Three detector classes

- **provider** — the existing prefix regexes, unchanged behavior.
- **usage** — secrets identified by how they are used, not who issued them:
  `bearer_token` (Authorization headers), `assignment_secret`
  (`key/token/password [:=] value`), `url_credential` (password component of
  `scheme://user:password@`). Usage detectors redact **only the value capture
  group** (`group: 1`) and emit the generic `[SECRET]` token; surrounding
  context words stay readable.
- **pii** — phone, private IPv4, email, unchanged.

Declaration order: providers → usage → pii. Overlap resolution operates on
the **redaction span** (the capture group when declared), so when an
assignment value *is* a provider token both detectors target the same span
and the earlier-declared provider wins the tie — the more informative token
(`[ANTHROPIC_KEY]`) is kept.

### 2. Deterministic entropy gate on usage detectors

`bearer_token` and `assignment_secret` candidates pass through a Shannon
entropy validator (`min_entropy: 3.5` bits/char on the value). Placeholders
in lessons *about* auth (`password = changeme`, `token := getToken`) stay
untouched; generated secrets (≥ ~14 distinct chars in 16) are redacted.
`url_credential` is not gated — the password position alone proves intent.
Entropy is a pure function of the value string: same input, same verdict, on
every machine. The determinism wedge of ADR-0007 holds.

### 3. Declarative embedded spec

Detectors live in `internal/scrub/spec.yaml` (go:embed), compiled at package
init (compile error = boot panic). The spec declares the pattern version; a
pinned-hash test (`TestSpecHashPinsVersion`) fails on any spec edit until the
version is bumped and the hash re-pinned — the old "NEVER reorder without
bumping" comment is now an enforced invariant. Adding a provider is one YAML
stanza plus one corpus case.

### 4. Engine package split

Engine, spec, entropy, and corpus move to `internal/scrub`. Save-path policy
(which fields are scrubbed, tag strict-reject, empty-after-scrub) stays in
`internal/store`, which re-exports `Scrub`/`ScrubReport`/`ScrubPatternVersion`
as aliases so consumers and the CLI are unchanged. Identifier fields gain the
tag policy: a scrub-detector hit in `task_type` or `session_id` strict-rejects
the save (`task_type_contains_secret` / `session_id_contains_secret`,
retryable) because identifiers are persisted unscrubbed by design.

### 5. Windowed scanning for prose-word needles

Usage-detector needles (`token`, `auth`, `key`) appear constantly in normal
lesson prose, so a needle hit cannot justify a full-body regex sweep. Windowed
scan: the regex runs only on a ~512-byte window after each needle hit, and
`assignment_secret` additionally requires a `:`/`=` within 64 bytes of the
needle (`guard_chars`) before opening a window. Regex match length is bounded
(`{8,256}`) so no match can be truncated at a window edge. Benchmarks: 10 KB
dense stress case stays at v2 cost (~1.1 ms); sparse realistic case +7%;
no-match case +~32 µs (one lowercase pass for case-insensitive needles).

## Consequences

- Unknown-vendor secrets, bearer headers, and connection-string passwords are
  now redacted — the largest false-negative class is closed.
- Pattern version bumps to 3. Existing rows stamped v1/v2 stay valid;
  `migrate --rescrub` re-applies the v3 set on demand (existing machinery).
- `[SECRET]` joins the redaction-token set; `isEmptyAfterScrub` already
  matches it via `\[[A-Z_]+\]`.
- Assignment detection carries inherent FP risk; the entropy gate plus
  negative corpus cases (`changeme`, `getToken`, prose mentions) pin the
  boundary. The gate threshold is spec-versioned like everything else.
- Non-ASCII input falls back from windowed/lowered scanning to full
  case-insensitive sweeps — correct but slower; acceptable for this corpus.
- gitleaks' ruleset remains a *reference* for future provider stanzas, not a
  dependency: importing it wholesale was rejected (re2-incompatible rules,
  upstream churn fighting spec versioning, FP profile tuned for source code
  rather than prose).
