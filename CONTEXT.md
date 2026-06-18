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

**Auto-session-summary**:
A session_summary composed and saved automatically at the end of a Claude Code Run rather than by an explicit agent decision. Not a separate Kind — it is a `session_summary` distinguished by `origin: auto` and routed to the reserved `claude_session` task_type so it gets its own retention budget and never pollutes workflow recall. Contrast: an ordinary session_summary is `origin: manual`.
_Avoid_: auto-summary kind, fifth kind, session log

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

**Origin**:
A Memory's provenance; either `manual` (default — saved by an explicit agent/operator decision) or `auto` (machine-composed by a session-end enforcement path). Orthogonal to Kind and Scope — it records *how* a Memory was authored, not what it means. Lets retention, ranking, and audit treat machine-forced writes differently without overloading Kind.
_Avoid_: source, type, automated flag

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

**Rule stub**:
A browse-Tier projection of a user_rule beyond the always-Tier cap — title only, expanded via mem_get.
_Avoid_: overflow rule, hidden rule, truncated rule

**Prune**:
The explicit, human-initiated deletion workflow for Memories; never automatic. Deletes either a filtered set (kind / task_type / age) or a single Memory by exact id; every deletion path routes through this one workflow so FTS stays in sync.
_Avoid_: cleanup, eviction, garbage collection, compaction

**Dupe cluster**:
A group of Memories likely capturing the same lesson, found by the relaxed offline Near-duplicate scan during Prune.
_Avoid_: duplicate group, family

### Scrub pipeline

**Scrub pipeline**:
The single-pass redaction stage that runs on every save against title, what, and learned before fingerprinting and dedupe. Secrets-first: developer credentials are the threat (ADR-0008); email/phone/private-IP coverage is incidental.
_Avoid_: filter, sanitizer, PII filter, PII scrub, masker

**Pattern**:
One of the 18 ordered detectors declared in the scrub spec, applied by the Scrub pipeline. Grouped into three Detector classes; declaration order is providers → usage → pii (pattern version 3). ssn + credit_card were dropped pre-v1.0 (high false-positive rate on free text). Overlap resolution: longer redaction span wins, tie → earlier declaration wins.
_Avoid_: rule, matcher, regex

**Detector class**:
The kind of evidence a Pattern keys on: `provider` (vendor prefix, e.g. `ghp_`), `usage` (how the value is used — bearer header, assignment, URL credential position), or `pii` (email, phone, private IP).
_Avoid_: pattern type, category

**Scrub spec**:
The embedded declarative file (`internal/scrub/spec.yaml`) that is the single source of truth for Patterns and the pattern version. Any edit forces a version bump via the pinned-hash test.
_Avoid_: pattern list, config

**Entropy gate**:
The deterministic Shannon-entropy validator (bits/char ≥ 3.5) that a usage-class candidate value must pass before redaction. Filters placeholders (`changeme`) from generated secrets. Pure function — preserves scrub determinism.
_Avoid_: randomness check, entropy filter

**Redaction token**:
The bracketed, per-category placeholder a Pattern emits in place of a match (`[EMAIL]`, `[AWS_KEY]`, `[JWT]`, …). Usage-class Patterns emit the generic `[SECRET]`.
_Avoid_: mask, placeholder, replacement

**ScrubReport**:
The per-save record of `redaction_count`, `per_pattern_counts`, `fields_redacted`, and `pattern_version`, included in the save response when any redaction occurred and persisted on the row as `scrub_counts`.
_Avoid_: scrub log, redaction summary

**Tag strict-reject**:
The save-rejection rule that fails any save whose tag matches a Pattern (`tag_contains_secret`, `retryable:true`). Tags are never auto-stripped.
_Avoid_: tag scrub, tag filter

**Identifier strict-reject**:
The same rule applied to `task_type` and `session_id` (`task_type_contains_secret`, `session_id_contains_secret`). Identifiers are routing keys stored unscrubbed, so a Pattern match rejects the save instead of redacting.
_Avoid_: identifier scrub

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

### Retrieval modes

**Context mode**:
The retrieval depth preset for a Context bundle request; one of `orient`, `deep`, or `refresh`. Defaults to `orient`. Exposed on both CLI (`--mode`) and MCP `mem_context`.
_Avoid_: context level, depth flag, verbosity

**orient**:
The default Context mode — always-tier full body + browse-tier title+snippet. Identical to pre-v1.1 context behavior.
_Avoid_: default mode, standard mode

**deep**:
A Context mode returning always-tier full body + all overflow user_rules expanded to full body + browse-tier items with full `what`+`learned`. No follow-up `get` calls needed.
_Avoid_: full mode, verbose mode, expanded mode

**refresh**:
A Context mode returning always-tier only (latest session_summary + ≤5 user_rules, no browse tier, no rule stubs). Designed for cheap mid-run re-anchor. Passing `--query` with `refresh` is a validation error.
_Avoid_: lite mode, fast mode, cheap mode

**TOON encoding** _(Deferred — ADR-0017)_:
An opt-in, token-saving tabular rendering of the Context bundle's browse Tier (`format: toon`), where the uniform browse rows share one field header instead of repeating keys per object. Additive only — JSON stays the default and the error envelope is always JSON. The always Tier and envelope stay JSON; only the browse array is rendered as TOON. Encoded at the edge, post-Scrub. _Deferred: the Phase-0 spike measured a net win below the ship bar on prose-heavy rows; parked pending a tightened re-measurement._
_Avoid_: format swap, serialization mode, compact JSON

**Expand signal**:
The `expand_count` + `last_expanded_at` (unix seconds) pair on a Memory, incremented by an agent-facing `get` (CLI `get`, MCP `mem_get`) and surviving force-save. Operator/TUI reads go through the non-counting fetch and never move it. Surfaced by `doctor --expand-stats` to inform future browse-tier sizing decisions.
_Avoid_: access count, hit count, view count, expand tracker

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

**Memory inspector**:
The interactive terminal browser (`droids-mem tui`) an operator uses to filter, search, read, and Prune the local corpus in-process. An operator tool, not an Agent client — its reads never move the Expand signal.
_Avoid_: dashboard, viewer, console, admin UI

### Session-end auto-save

**Staged summary**:
A complete, model-composed `mem_save` payload for an Auto-session-summary, written to a file under `~/.droids-mem/` during a Claude Code Run and flushed to the store at session end. Lives outside the data model — the DB only ever sees the final committed Memory, never the draft.
_Avoid_: draft row, pending summary, buffer

**Intake gate**:
The dual condition a Claude Code Run must pass before its Staged summary is persisted as an Auto-session-summary: a mechanical change-count threshold AND the model's judgment that the Run was worth recalling. Failing either yields no Auto-session-summary. The primary defense against retaining low-value sessions — junk is kept out at intake, not evicted later.
_Avoid_: filter, gate check, save guard

**Meaningful change**:
A counted Run action (Edit / Write / Bash exec, or a git commit) used as the mechanical half of the Intake gate. Tallied DB-free in a sentinel file via the Claude Code `PostToolUse` hook; the same marker drives the Stop-hook staleness check.
_Avoid_: edit count, activity, mutation

**Checkpoint**:
An enforced re-stage of the Staged summary at a stopping point (Claude Code `Stop` hook, fired per turn) once Meaningful changes since the last stage cross the Intake gate threshold. The hook blocks once per threshold-crossing to force the re-stage, then unblocks. Makes staging a per-stopping-point guarantee, not a single end-of-Run gamble.
_Avoid_: autosave, snapshot, commit

**Recovery flush**:
The session-start step that flushes an orphaned Staged summary left by a crashed Run to the store, then cleans the file — recovering the last Checkpoint instead of discarding it. A 7-day TTL reaps any staged file too corrupt to flush.
_Avoid_: replay, restore, crash recovery

**Relevance-gated pull**:
The read-side enforcement that guarantees prior Memories about the current task surface when the agent starts it: a Claude Code `UserPromptSubmit` hook runs a score-floored search over the prompt and injects a hit only above a relevance floor, deduped to once per Memory per Session. Distinct from a recency push — applicability, not recency, is the gate; below the floor it injects nothing.
_Avoid_: auto-recall, context injection, recency push

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
- "characters" was used for two different size units — resolved: **Field cap** limits are measured in bytes; the browse snippet budget is measured in runes. Never say "characters" for either.
