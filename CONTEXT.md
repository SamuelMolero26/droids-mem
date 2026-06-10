# droids-mem

Local-first persistent memory for AI agents via SQLite + FTS5: structured, deduplicated, PII-scrubbed lessons that survive across Runs so agents stop relearning the same things.

## Language

### Memory unit

**Memory**:
A structured lesson saved for future agent runs.
_Avoid_: note, record, entry, log, document

**Kind**:
The category of a Memory; exactly one of `error_resolution`, `task_pattern`, `user_rule`, `session_summary`.
_Avoid_: type, category, class

**error_resolution**:
A Memory describing a problem the agent hit and the fix that worked.
_Avoid_: bugfix, incident, error log

**task_pattern**:
A Memory capturing a repeatable successful approach worth reusing.
_Avoid_: recipe, template, playbook

**user_rule**:
A Memory holding a user correction or stable preference the agent must persist.
_Avoid_: rule, override, setting

**session_summary**:
A Memory written once at the end of a Run capturing intent, outcome, and what to remember next time.
_Avoid_: report, recap, postmortem

### Grouping and scope

**Session**:
The grouping of all Memories saved during one Run, identified by `session_id`.
_Avoid_: run group, batch, conversation

**Run**:
A single agent execution from start to end that produces exactly one session_summary.
_Avoid_: invocation, session (when referring to execution itself)

**task_type**:
Free-form workflow tag that scopes Context bundle retrieval and session_summary retention.
_Avoid_: domain, category, workflow, job

**Scope**:
A Memory's visibility classification; either `personal` (private to the user) or `shared` (eligible for cross-agent reuse). Defaults to `shared` and is forward-compat for the v1.1 workspace model.
_Avoid_: visibility, privacy, audience

### Dedupe and retrieval

**Fingerprint**:
Deterministic hash over normalized `task_type + kind + title + learned` used as Layer 1 exact-duplicate detection.
_Avoid_: hash, checksum, digest

**Near-duplicate**:
A new Memory whose Jaccard similarity against the best BM25 candidate exceeds threshold and is suppressed as Layer 2 dedupe.
_Avoid_: similar, fuzzy match

**force**:
A save flag, used for HITL correction, that overwrites a Fingerprint-matched Memory instead of skipping it.
_Avoid_: overwrite, upsert, replace

**Match echo**:
Fields `matched_title` and `matched_learned` returned on a skip response so callers see exactly which existing Memory the new save collided with.
_Avoid_: dedupe hint, conflict preview

**Context bundle**:
The two-Tier payload returned at the start of a Run to orient the agent for a task_type.
_Avoid_: snapshot, dump, recall, history

**Tier**:
The role a Memory plays in the Context bundle; either `always` (full body) or `browse` (snippet only, expand via mem_get).
_Avoid_: slot, bucket, layer

### Scrub pipeline

**Scrub pipeline**:
The single-pass redaction stage that runs on every save against title, what, and learned before fingerprinting and dedupe.
_Avoid_: filter, sanitizer, PII filter, masker

**Pattern**:
One of the 13 ordered detectors (pem_key → jwt → aws_key → github_token → stripe_key → slack_token → anthropic_key → openai_key → ssn → credit_card → phone → private_ipv4 → email) the Scrub pipeline applies. Overlap resolution: longer wins, tie → earlier declaration wins.
_Avoid_: rule, matcher, detector, regex

**Redaction token**:
The bracketed, per-category placeholder a Pattern emits in place of a match (`[EMAIL]`, `[AWS_KEY]`, `[JWT]`, …).
_Avoid_: mask, placeholder, replacement

**ScrubReport**:
The per-save record of `redaction_count`, `per_pattern_counts`, `fields_redacted`, and `pattern_version`, included in the save response when any redaction occurred and persisted on the row as `scrub_counts`.
_Avoid_: scrub log, redaction summary

**Tag strict-reject**:
The save-rejection rule that fails any save whose tag matches a Pattern (`tag_contains_secret`, `retryable:true`). Tags are never auto-stripped.
_Avoid_: tag scrub, tag filter

**Empty-after-scrub**:
The save-rejection rule that fails any save whose `learned` field is empty after the Scrub pipeline runs (`scrub_emptied_learned`).
_Avoid_: empty-body reject

**Field cap**:
The per-field byte limit enforced before scrub (title=200, what=8192, learned=4096, tags=500). Exceeding any cap returns `field_too_large`.
_Avoid_: size limit, max length

### Migration and gating

**Scrub baseline**:
The `meta.scrub_baseline_complete=1` sentinel that records a database has been processed by the v1.0 Scrub pipeline. Required for the v1.0 binary to boot.
_Avoid_: scrub flag, baseline marker

**Boot gate**:
The startup check that refuses to run if the schema is at v1.0+ but the Scrub baseline sentinel is absent. Bypassed only by the `migrate` subcommand.
_Avoid_: startup check, safety gate

**Rescrub**:
`migrate --rescrub`: the atomic per-DB operation that walks every row through the Scrub pipeline, rewrites text fields, re-fingerprints, and sets the Scrub baseline. Contrast: `--no-rescrub` sets the baseline without rewriting (acknowledges plaintext).
_Avoid_: backfill, reprocess, replay

### Agent access

**Subprocess transport**:
The default access path where the agent spawns the `droids-mem` CLI per call and reads JSON.
_Avoid_: shell tool, exec tool

**MCP bridge**:
The alternate access path that exposes a fixed set of memory operations over JSON-RPC to remote agent runtimes. Started via `droids-mem serve` (same binary as the CLI).
_Avoid_: API server, gateway, daemon

**ensure-server**:
The idempotent CLI operation an Agent client calls before MCP requests: pings `/healthz`, otherwise spawns `droids-mem serve` detached and waits for ready.
_Avoid_: bootstrap, start, launch

**Identity proof**:
The `/identity?nonce=<n>` endpoint that answers `HMAC-SHA256(token, nonce)`, letting `ensure-server` verify a listener actually holds the bearer token before reporting `already_running`. Defends against port squatting.
_Avoid_: handshake, attestation

**Agent client**:
Any program holding a Run that talks to the store via Subprocess transport or MCP bridge.
_Avoid_: caller, consumer, user (overloaded with end-user)

## Relationships

- A **Run** belongs to exactly one **Session** and produces exactly one **session_summary**
- A **Session** groups many **Memories**, each of one **Kind** and one **Scope**
- A **Memory** has exactly one **Fingerprint**; identical Fingerprints collapse via Layer 1 dedupe
- A save passes through **Field cap** check → **Tag strict-reject** → **Scrub pipeline** → **Empty-after-scrub** check → **Fingerprint** → near-duplicate check, then persists with a **ScrubReport** if any **Pattern** matched
- A **Pattern** match is replaced by its **Redaction token**; counts roll up into the **ScrubReport**
- A skip response carries a **Match echo** when an existing Memory caused Layer 1 or Layer 2 dedupe
- A `session_summary` **Memory** is scoped by **task_type**; only the 5 newest per task_type are retained
- A **Context bundle** for a **task_type** has an always **Tier** (latest session_summary + ≤5 user_rules in full) and a browse **Tier** (≤10 error_resolution + ≤10 task_pattern as title + snippet, ranked by BM25)
- A v1.0+ schema requires the **Scrub baseline** sentinel; the **Boot gate** refuses to start without it, and **Rescrub** is the only path that sets it after an upgrade
- An **Agent client** reaches the store through either **Subprocess transport** or the **MCP bridge** and owns the **Session** by threading `session_id` across saves
- The **MCP bridge** is kept alive by the host OS service manager (primary); an **Agent client** calls **ensure-server** as a fallback, which validates listener ownership via **Identity proof** before declaring the bridge healthy

## Example dialogue

> **Dev:** "Agent hits the same HubSpot phone-field bug twice in one **Run** — do we save two **Memories**?"
> **Domain expert:** "No. Same **Fingerprint** → Layer 1 dedupe skips the second save. Even if wording drifts, the Layer 2 check catches it as a **Near-duplicate**."
> **Dev:** "When the second save is skipped, how does the agent know which Memory it collided with?"
> **Domain expert:** "The skip response carries a **Match echo** — `matched_title` and `matched_learned` — so the agent can show the user what already exists without a second lookup."
> **Dev:** "What if the bug report the agent tries to save has the customer's email and phone in the body?"
> **Domain expert:** "The **Scrub pipeline** runs before fingerprint. Email becomes `[EMAIL]`, phone becomes `[PHONE]` — the **Redaction tokens** are what get persisted and what feed the Fingerprint. A **ScrubReport** is attached to the save response so the agent sees what was redacted."
> **Dev:** "Can it stick the email into a tag instead?"
> **Domain expert:** "No — **Tag strict-reject**. Tags that match any **Pattern** fail the save with `tag_contains_secret`. Tags are never auto-stripped; the agent has to fix the tag and retry."
> **Dev:** "What if scrub eats the entire `learned` body?"
> **Domain expert:** "**Empty-after-scrub** rejects the save with `scrub_emptied_learned`. The lesson has to be rewritten without the PII."
> **Dev:** "If the user corrects the fix mid-Run?"
> **Domain expert:** "Resave with `force`. Same Fingerprint match, but the existing Memory is overwritten in place."
> **Dev:** "Does that corrected Memory show up in the next **Run**'s **Context bundle**?"
> **Domain expert:** "If it's a `user_rule` or `session_summary`, the always **Tier** carries it in full. If it's an `error_resolution` or `task_pattern`, the browse Tier shows a snippet and the agent calls `mem_get` for the full body."
> **Dev:** "What about a brand-new install of v1.0 on top of an old DB?"
> **Domain expert:** "The **Boot gate** refuses to start until the **Scrub baseline** sentinel is set. The operator runs `migrate --rescrub` to walk every existing row through the **Scrub pipeline** atomically, or `--no-rescrub` to acknowledge plaintext and just set the sentinel."
> **Dev:** "Does it matter whether the **Agent client** is using **Subprocess transport** or the **MCP bridge**?"
> **Domain expert:** "No — both wrap the same store. The only difference is who mints the `session_id`: in MCP, `mem_context` returns one; with Subprocess, `save` mints one on the first call. The Agent client is responsible for threading it through subsequent calls either way."
> **Dev:** "What stops the **MCP bridge** from being down when an **Agent client** wakes up?"
> **Domain expert:** "The host OS service manager keeps the bridge alive and restarts it on crash. If that's down for any reason, the Agent client calls **ensure-server** as a fallback — idempotent: if `/healthz` answers and **Identity proof** confirms the listener holds the token, it returns immediately; otherwise it spawns `droids-mem serve` and waits for ready."

## Flagged ambiguities

- "session" was used for both **Session** (grouping) and **Run** (execution) — resolved: a Run produces one Session; refer to execution as a Run.
- "type" was overloaded between **Kind** (4-value enum) and **task_type** (free-form workflow tag) — resolved: never say "type" unqualified; use Kind or task_type.
- "context" was used for both the `mem_context` operation and any retrieval result — resolved: reserve **Context bundle** for the returned payload; lowercase `context` refers only to the operation.
- "duplicate" conflated exact Fingerprint matches with BM25 paraphrases — resolved: exact = duplicate (Layer 1); paraphrase = **Near-duplicate** (Layer 2).
- "scope" was used informally to mean both **Scope** (personal | shared on a Memory) and v1.1 workspace boundaries — resolved: lowercase scope refers to the Memory column only; v1.1 workspace boundaries get their own term when ADR-0005 ships.
- "scrub" was used to mean both the **Scrub pipeline** (the always-on save stage) and the `scrub` CLI (an ad-hoc human debugging tool that does not touch the DB) — resolved: capital-S Scrub pipeline = pipeline; lowercase `scrub` command = CLI.
- "migrate" conflated **PRAGMA user_version** schema migrations (automatic on `db.Init`) and the `migrate` subcommand that establishes the **Scrub baseline** — resolved: schema migration runs at boot; `migrate` subcommand exists solely to satisfy the **Boot gate** via **Rescrub** / no-rescrub.
