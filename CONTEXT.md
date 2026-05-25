# droids-mem

Local-first persistent memory for AI agents via SQLite + FTS5: structured, deduplicated lessons that survive across Runs so agents stop relearning the same things.

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

**Context bundle**:
The two-Tier payload returned at the start of a Run to orient the agent for a task_type.
_Avoid_: snapshot, dump, recall, history

**Tier**:
The role a Memory plays in the Context bundle; either `always` (full body) or `browse` (snippet only, expand via mem_get).
_Avoid_: slot, bucket, layer

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

**Agent client**:
Any program holding a Run that talks to the store via Subprocess transport or MCP bridge.
_Avoid_: caller, consumer, user (overloaded with end-user)

## Relationships

- A **Run** belongs to exactly one **Session** and produces exactly one **session_summary**
- A **Session** groups many **Memories**, each of one **Kind**
- A **Memory** has exactly one **Fingerprint**; identical Fingerprints collapse via Layer 1 dedupe
- A `session_summary` **Memory** is scoped by **task_type**; only the 5 newest per task_type are retained
- A **Context bundle** for a **task_type** has an always **Tier** (latest session_summary + all user_rules in full) and a browse **Tier** (top error_resolution + task_pattern as title + snippet, ranked by BM25)
- An **Agent client** reaches the store through either **Subprocess transport** or the **MCP bridge** and owns the **Session** by threading `session_id` across saves
- The **MCP bridge** is kept alive by the host OS service manager (primary); an **Agent client** calls **ensure-server** as a fallback before issuing JSON-RPC requests in case the OS service is down

## Example dialogue

> **Dev:** "Agent hits the same HubSpot phone-field bug twice in one **Run** — do we save two **Memories**?"
> **Domain expert:** "No. Same **Fingerprint** → Layer 1 dedupe skips the second save. Even if wording drifts, the Layer 2 check catches it as a **Near-duplicate**."
> **Dev:** "If the user corrects the fix mid-Run?"
> **Domain expert:** "Resave with `force`. Same Fingerprint match, but the existing Memory is overwritten in place."
> **Dev:** "Does that corrected Memory show up in the next **Run**'s **Context bundle**?"
> **Domain expert:** "If it's a `user_rule` or `session_summary`, the always **Tier** carries it in full. If it's an `error_resolution` or `task_pattern`, the browse Tier shows a snippet and the agent calls `mem_get` for the full body."
> **Dev:** "Does it matter whether the **Agent client** is using **Subprocess transport** or the **MCP bridge**?"
> **Domain expert:** "No — both wrap the same store. The only difference is who mints the `session_id`: in MCP, `mem_context` returns one; with Subprocess, `save` mints one on the first call. The Agent client is responsible for threading it through subsequent calls either way."
> **Dev:** "What stops the **MCP bridge** from being down when an **Agent client** wakes up?"
> **Domain expert:** "The host OS service manager keeps the bridge alive and restarts it on crash. If that's down for any reason, the Agent client calls **ensure-server** as a fallback — idempotent: if `/healthz` answers it returns immediately, otherwise it spawns `droids-mem serve` and waits for ready."

## Flagged ambiguities

- "session" was used for both **Session** (grouping) and **Run** (execution) — resolved: a Run produces one Session; refer to execution as a Run.
- "type" was overloaded between **Kind** (4-value enum) and **task_type** (free-form workflow tag) — resolved: never say "type" unqualified; use Kind or task_type.
- "context" was used for both the `mem_context` operation and any retrieval result — resolved: reserve **Context bundle** for the returned payload; lowercase `context` refers only to the operation.
- "duplicate" conflated exact Fingerprint matches with BM25 paraphrases — resolved: exact = duplicate (Layer 1); paraphrase = **Near-duplicate** (Layer 2).
