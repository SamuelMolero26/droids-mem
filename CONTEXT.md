# droids-mem

Local-first Go binary giving AI agents persistent memory via SQLite + FTS5. Stores structured, deduplicated lessons so agents stop forgetting what they learned between runs.

## Language

### Memory unit

**Memory**:
A structured lesson saved for future agent runs. Has `id`, `kind`, `title`, `what`, `learned`, `tags`.
_Avoid_: note, record, entry, log, document

**Kind**:
The category of a Memory. Exactly one of `error_resolution`, `task_pattern`, `user_rule`, `session_summary`.
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
A Memory written once at end of a run capturing intent, outcome, and what to remember next time.
_Avoid_: report, recap, postmortem

### Grouping and scope

**Session**:
The grouping of all Memories saved during one agent run. Identified by `session_id` (ULID).
_Avoid_: run group, batch, conversation

**Run**:
A single agent execution from start to end. Produces exactly one `session_summary`.
_Avoid_: invocation, session (when referring to execution itself)

**task_type**:
Free-form string naming the kind of work a Run performs (e.g. `crm_upload`). Primary scope key for `context` retrieval and session_summary retention.
_Avoid_: domain, category, workflow, job

### Dedupe and retrieval

**Fingerprint**:
Deterministic SHA-256 over normalized `title`+`learned` concatenated with `task_type` and `kind`. Layer 1 exact-duplicate detection.
_Avoid_: hash, checksum, digest

**Near-duplicate**:
New Memory whose Jaccard similarity over normalized tokens against the best BM25 candidate exceeds threshold (0.85). Suppressed as Layer 2 dedupe.
_Avoid_: similar, fuzzy match

**force**:
Save flag indicating HITL correction. Overwrites Memory matched by Fingerprint instead of skipping.
_Avoid_: overwrite, upsert, replace

**Context bundle**:
Two-tier payload returned at start of a Run to orient the agent.
- **Always tier**: latest `session_summary` (full body) + ALL `user_rule` Memories for the task_type (full body). Never truncated — these are critical state.
- **Browse tier**: top `error_resolution` + `task_pattern` Memories by BM25 rank, returned as title + 120-char snippet of `what`. Agent calls `get --id` for full body.
_Avoid_: snapshot, dump, recall, history

**Tier**:
Field on each returned ContextMemory indicating its role: `"always"` (full body, critical state) or `"browse"` (snippet only, agent expands on demand).
_Avoid_: slot, bucket, layer

### Storage

**memories table**:
SQLite source-of-truth table for all Memories. All exact filtering, dedupe, and timestamps read from here.
_Avoid_: store, primary table

**memories_fts**:
SQLite FTS5 external-content virtual table indexing `title`, `what`, `learned`, `tags` of `memories`. Search-only — never source of truth.
_Avoid_: index table, search table (when ambiguous with `memories`)

## Relationships

- A **Run** belongs to exactly one **Session** and produces exactly one **session_summary**
- A **Session** groups many **Memories**, each of one **Kind**
- A **Memory** has exactly one **Fingerprint**; identical Fingerprints collapse via Layer 1 dedupe
- A **Memory** of kind `session_summary` is scoped by **task_type**; only 5 newest per task_type retained
- The **Context bundle** for a `task_type` is composed of an always **Tier** (full-body session_summary + user_rules) and a browse **Tier** (snippet-only top error_resolution + task_pattern, BM25 ranked)
- **memories_fts** mirrors **memories** via AFTER INSERT/UPDATE/DELETE triggers — never written directly

## Example dialogue

> **Dev:** "Agent hits same HubSpot phone field bug twice in one Run — do we save two **Memories**?"
> **Domain expert:** "No. Same **Fingerprint** → Layer 1 dedupe skips the second save. Even if wording drifts, Layer 2 BM25 check catches it as a **Near-duplicate**."
> **Dev:** "And if user corrects the fix mid-Run?"
> **Domain expert:** "Resave with `force: true`. Same Fingerprint match, but existing record's `title`, `what`, `learned`, `tags`, and `updated_at` overwrite in place. Update trigger keeps **memories_fts** consistent."
> **Dev:** "Does that corrected Memory show up in next **Run**'s **Context bundle**?"
> **Domain expert:** "If it's a `user_rule` or `session_summary`, it lands in the always **Tier** with full body — those are never truncated. If it's an `error_resolution` or `task_pattern`, it lands in the browse Tier as a snippet if it ranks in the top 10 by BM25 against `query` or tokenized `task_type`. Agent reads the snippet, then calls `get --id` for full body if relevant."

## Flagged ambiguities

- "session" used for both **Session** (grouping) and **Run** (execution) — resolved: a Run produces one Session; refer to execution as a Run.
- "type" overloaded between **Kind** (4-value enum) and **task_type** (free-form workflow tag) — resolved: never say "type" unqualified; use Kind or task_type.
- "context" used for both the `context` command and any retrieval result — resolved: reserve **Context bundle** for returned payload; lowercase `context` only refers to command.
- "duplicate" conflated exact Fingerprint matches with BM25 paraphrases — resolved: exact = duplicate (Layer 1), paraphrase = **Near-duplicate** (Layer 2).
