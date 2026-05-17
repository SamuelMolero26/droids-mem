# droids-mem — Product Requirements Document

> **Version:** 1.1 · **Status:** Draft · **Last Updated:** May 2026

---

## Table of Contents

1. [Overview](#1-overview)
2. [Problem Statement](#2-problem-statement)
3. [Goals & Success Metrics](#3-goals--success-metrics)
4. [Product Shape](#4-product-shape)
5. [Core Workflows](#5-core-workflows)
6. [Data Model](#6-data-model)
7. [Memory Rules](#7-memory-rules)
8. [Local Command Surface](#8-local-command-surface)
9. [Development Milestones](#9-development-milestones)
10. [Risks & Mitigations](#10-risks--mitigations)
11. [Out of Scope](#11-out-of-scope)
12. [Glossary](#12-glossary)

---

## 1. Overview

**droids-mem** is a lightweight, local-first Go binary that gives AI agents a persistent memory layer backed by SQLite with FTS5 full-text search.

The goal of V1 is not distributed infrastructure, deployment scoping, or service orchestration. The goal is to get the **memory tool itself** right: what an agent saves, how memories are structured, how duplicate/noisy memories are avoided, and how useful context is retrieved before the next task run.

At its core, droids-mem helps an agent do three things well:

- save a useful lesson after it learns something
- search past lessons when it encounters a similar task or issue
- load a small, high-signal context snapshot at the start of a run

The binary should work fully on a local machine with zero external dependencies beyond a SQLite file.

---

## 2. Problem Statement

AI agents often repeat the same mistakes because each run starts with no persistent operational memory. Even when an agent successfully resolves an issue once, that lesson is frequently lost unless a human hardcodes the fix somewhere else.

For V1, the core problem is not networking or multitenancy. The real problem is:

**How do we give an agent a memory system that stays useful instead of turning into junk?**

That breaks into four practical challenges:

**No retained lessons.** Agents forget successful resolutions, mappings, and user corrections between runs.

**Memory noise.** If agents save everything, retrieval quality collapses. Logs, repeated summaries, and vague notes become clutter.

**Weak retrieval.** If search returns too much irrelevant text, the memory layer becomes a distraction instead of an asset.

**Unclear save behavior.** If the rules for what to save are not explicit, memory quality will vary dramatically between runs.

Droids-mem V1 solves these problems by enforcing a small set of structured memory types, a clear save policy, and fast local retrieval over SQLite + FTS5.

---

## 3. Goals & Success Metrics

### 3.1 Goals

- Build a **local-first memory binary** that works without any external services
- Define a **clear, durable memory contract** for agent-generated memories
- Keep retrieval fast enough to run at the start of every task without noticeable delay
- Keep memory human-readable and easy to inspect in SQLite
- Prevent memory quality decay through structured fields and duplicate suppression

### 3.2 Success Metrics

| Metric | Target |
|---|---|
| Local search latency (p95) | < 100ms |
| Binary startup time | < 200ms |
| Binary size (compiled) | < 20MB |
| Duplicate memory rate | ≤ 10% of saved memories are near-duplicates |
| Context payload size | ≤ 10 memory items returned by `context` |
| Storage footprint (first 90 days, single-user local usage) | < 25MB |

---

## 4. Product Shape

### 4.1 Core Shape

Droids-mem V1 is a **single Go binary** that manages:

- a local SQLite database
- an FTS5 search index
- a small command surface for saving, searching, and loading context

The binary should be usable in any of these modes over time:

- CLI invocation
- stdio tool wrapper
- embedded/local process integration

V1 does **not** need to commit to one long-term transport layer yet. The backbone is the local memory engine itself.

### 4.2 Design Principles

**Single binary, zero external dependencies.** No separate database server, vector service, or background platform dependency.

**Local-first by default.** The tool should be easy to run and debug on a laptop before it is integrated into a larger runtime.

**Structured memory beats raw transcripts.** The tool stores curated lessons, not raw logs or chain-of-thought.

**Normal table + FTS index.** SQLite should use a standard `memories` table as the source of truth and an FTS5 table for search. FTS5 should not be the primary store.

**Precision over cleverness.** Retrieval should prefer small, relevant, interpretable results rather than ambitious but noisy ranking behavior.

**Readable by humans.** A human should be able to inspect the SQLite file and understand what the agent learned.

---

## 5. Core Workflows

### 5.1 Save a Memory

When an agent resolves an issue, discovers a repeatable mapping, receives a user correction, or finishes a task run, it saves a structured memory.

The save path should:

1. validate required fields
2. normalize text used for deduplication
3. compute a fingerprint
4. detect likely duplicates
5. insert a new memory only if it is sufficiently distinct

### 5.2 Search Memory

When an agent hits a familiar problem or starts a related task, it searches memory using concise domain-specific keywords.

Search should support:

- full-text lookup over title, what, learned, and tags
- optional filtering by `task_type`
- optional filtering by `kind`
- ranking by relevance with recency as a light tie-breaker

### 5.3 Load Start-of-Run Context

At the start of a task run, the agent should not receive a giant dump of memory.

Instead, droids-mem returns a compact context bundle using **priority slots**:

| Slot | Kind | Selection |
|---|---|---|
| 1 | `session_summary` | Latest for matching `task_type` |
| up to 3 | `error_resolution` | FTS ranked by relevance |
| up to 2 | `user_rule` | Most recent |
| up to 2 | `task_pattern` | FTS ranked by relevance |

Before returning, all candidates are deduplicated by `id`. If the same memory qualifies for multiple slots, it appears once in the highest-priority slot. Total result count is capped by the `limit` param (default 8).

The point of `context` is orientation, not exhaustive recall.

### 5.4 End a Run

At the end of every run, the agent saves one `session_summary` memory that captures:

- what the run tried to do
- what succeeded or failed
- what should be remembered next time

**Retention policy:** On each new `session_summary` save for a given `task_type`, the system counts existing summaries for that `task_type`. If count exceeds 5, the oldest by `created_at` is deleted. This bounds summary accumulation to 5 per task_type with no manual intervention.

This keeps continuity between runs without requiring full transcript replay.

---

## 6. Data Model

### 6.1 `memories` Table (Source of Truth)

```sql
CREATE TABLE memories (
    id            TEXT PRIMARY KEY,
    session_id    TEXT NOT NULL,
    task_type     TEXT NOT NULL,
    kind          TEXT NOT NULL,      -- error_resolution | task_pattern | user_rule | session_summary
    title         TEXT NOT NULL,
    what          TEXT NOT NULL,
    learned       TEXT NOT NULL,
    tags          TEXT,               -- space-delimited tokens (e.g. "hubspot phone field-mapping")
    fingerprint   TEXT NOT NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);
```

`tags` must be space-delimited, not JSON. FTS5 tokenizes on whitespace; JSON brackets/quotes break token matching.

`updated_at` is set to `created_at` on insert via an AFTER INSERT trigger. It is only mutated on forced overwrites (HITL corrections). Do not use `DEFAULT 0` — epoch timestamp corrupts recency-based tie-breaking.

This is the canonical record. All exact filtering, deduplication, timestamps, and future migrations should use this table.

### 6.2 `memories_fts` Table (Search Index)

```sql
CREATE VIRTUAL TABLE memories_fts USING fts5(
    title,
    what,
    learned,
    tags,
    content='memories',
    content_rowid='rowid'
);
```

This follows the SQLite **External Content Table** pattern (see SQLite FTS5 docs, "External Content Tables"). The FTS index does not auto-sync — three triggers are required at schema init:

```sql
-- keep FTS in sync with memories table
CREATE TRIGGER memories_ai AFTER INSERT ON memories BEGIN
  INSERT INTO memories_fts(rowid, title, what, learned, tags)
  VALUES (new.rowid, new.title, new.what, new.learned, new.tags);
END;

CREATE TRIGGER memories_ad AFTER DELETE ON memories BEGIN
  INSERT INTO memories_fts(memories_fts, rowid, title, what, learned, tags)
  VALUES ('delete', old.rowid, old.title, old.what, old.learned, old.tags);
END;

CREATE TRIGGER memories_au AFTER UPDATE ON memories BEGIN
  INSERT INTO memories_fts(memories_fts, rowid, title, what, learned, tags)
  VALUES ('delete', old.rowid, old.title, old.what, old.learned, old.tags);
  INSERT INTO memories_fts(rowid, title, what, learned, tags)
  VALUES (new.rowid, new.title, new.what, new.learned, new.tags);
END;
```

FTS queries return `rowid` (SQLite internal integer). Bridge to the `memories` table via:

```sql
SELECT m.* FROM memories m
WHERE m.rowid IN (
  SELECT rowid FROM memories_fts WHERE memories_fts MATCH ?
)
ORDER BY rank;
```

Command responses always expose the TEXT `id` field — callers never see or need `rowid`.

### 6.3 Memory Kinds

| Kind | When Used | Example |
|---|---|---|
| `error_resolution` | Agent encountered and fixed an error | API rejected `phone_number`; use `phone` |
| `task_pattern` | Agent discovered a repeatable successful pattern | CSV import works when dates are normalized to ISO-8601 |
| `user_rule` | User correction or stable preference should persist | Always abbreviate Company as Co. |
| `session_summary` | End-of-run summary | Import completed; blank company rows still need review |

### 6.4 Optional Future Fields

These are intentionally deferred from V1 unless they become necessary during implementation:

- `why`
- `where_`
- `source_tool`
- `confidence`
- `expires_at`
- `supersedes_id`

V1 should stay small unless real usage proves these fields are needed.

### 6.5 Fingerprint Strategy

Each memory gets a deterministic `fingerprint` derived from normalized fields. V1 uses a two-layer dedupe approach:

**Layer 1 — Exact fingerprint match**

Normalization pipeline (applied to `title` + `learned` before hashing):

1. lowercase
2. trim whitespace
3. collapse internal whitespace to single space
4. strip punctuation
5. sort words alphabetically
6. concatenate with `task_type` and `kind`
7. SHA-256 → hex string

A memory with identical fingerprint is skipped or force-overwritten (see §8.1).

**Layer 2 — Pre-save BM25 check**

Before inserting, run an FTS search using the new memory's `title` + `learned` as the query. If the top result has a BM25 rank above a defined threshold, treat the new memory as a near-duplicate and skip insertion. This catches paraphrased lessons that produce different fingerprints but carry the same meaning.

Fuzzy duplicate detection (SimHash, shingle similarity) is deferred to post-V1.

---

## 7. Memory Rules

### 7.1 Always Save

The agent should always save:

- an error it successfully resolved
- a repeatable transformation or mapping learned for the first time
- a user correction or user-specific rule
- one `session_summary` at the end of the run

### 7.2 Never Save

The agent should never save:

- raw tool call logs
- intermediate reasoning or chain-of-thought
- full transcripts
- giant unstructured blobs of text
- duplicate memories with no new lesson
- sensitive raw data when a generalized lesson can be stored instead

### 7.3 Required Quality Bar

A valid memory should answer two questions clearly:

- **What happened?**
- **What should the agent do next time?**

If a memory does not teach a reusable lesson, it should not be saved.

### 7.4 Duplicate Handling

Duplicate control is a core part of V1.

When a new memory is saved, droids-mem should:

1. compute the fingerprint
2. check for an existing memory with the same fingerprint
3. either skip the write, or refresh metadata if the project later chooses to support that behavior

V1 may start with exact fingerprint dedupe only. Fuzzy duplicate detection can wait.

---

## 8. Local Command Surface

The binary should expose a small local command surface. The exact transport may evolve, but the operations should be stable.

### 8.1 `save`

Stores one structured memory.

**Input shape**

```json
{
  "session_id": "sess_001",
  "task_type": "crm_upload",
  "kind": "error_resolution",
  "title": "HubSpot phone field mapping",
  "what": "Upload failed because the target field was phone_number",
  "learned": "Map Phone Number to phone",
  "tags": ["hubspot", "field-mapping", "phone"]
}
```

**Optional field**

| Field | Type | Description |
|---|---|---|
| `force` | bool | If `true`, overwrite existing memory with same fingerprint instead of skipping |

`force` is the HITL correction path. When a human corrects agent behavior, the agent resaves the memory with `force: true`. The system finds the existing record by fingerprint, updates `title`, `what`, `learned`, `tags`, and `updated_at` in place, and syncs the FTS index via the update trigger.

**Expected behavior**

- validate required fields
- normalize text
- compute fingerprint (layer 1 dedupe)
- run pre-save BM25 check (layer 2 dedupe)
- if `force=false` (default): skip if fingerprint or BM25 threshold matched
- if `force=true`: overwrite matched record; insert if no match found
- return saved or updated memory metadata

**Response shapes**

Successful save:
```json
{ "status": "saved", "id": "mem_01J...", "session_id": "sess_01J..." }
```

Duplicate skipped (layer 1 — exact fingerprint):
```json
{ "status": "skipped", "reason": "duplicate", "matched_id": "mem_01J..." }
```

Near-duplicate skipped (layer 2 — BM25):
```json
{ "status": "skipped", "reason": "near_duplicate", "matched_id": "mem_01J...", "score": -18.4 }
```

Force overwrite applied:
```json
{ "status": "updated", "id": "mem_01J...", "session_id": "sess_01J..." }
```

Validation failure:
```json
{ "status": "error", "code": "validation_failed", "field": "kind", "message": "kind must be one of: error_resolution, task_pattern, user_rule, session_summary" }
```

**Session ID handling**

`session_id` is optional on input. If omitted, the binary generates a ULID and returns it in the response. The agent should reuse the returned `session_id` for all subsequent saves in the same run to group memories under one session.

### 8.2 `search`

Searches stored memories.

**Input shape**

```json
{
  "query": "hubspot phone mapping",
  "task_type": "crm_upload",
  "kind": "error_resolution",
  "limit": 5
}
```

**Expected behavior**

- search FTS index
- filter on exact columns from `memories`
- return concise results with title, learned, kind, and created_at

### 8.3 `context`

Returns a compact set of memories to prime the next run.

**Input shape**

```json
{
  "task_type": "crm_upload",
  "query": "hubspot phone field mapping",
  "limit": 8
}
```

`query` is optional. If omitted, the binary tokenizes `task_type` and uses those tokens as the FTS query for relevance ranking. If provided, `query` is used directly for FTS slots (`error_resolution`, `task_pattern`). `user_rule` and `session_summary` slots use recency only — no FTS ranking applied.

**Expected behavior**

- fetch latest `session_summary` for `task_type` (recency)
- fetch top `error_resolution` matches via FTS on `query`
- fetch most recent `user_rule` memories
- fetch top `task_pattern` matches via FTS on `query`
- deduplicate by `id` across all slots
- trim to `limit`

Return a bundle shaped like:

```json
{
  "last_session": {
    "id": "mem_01J...",
    "title": "...",
    "learned": "...",
    "created_at": 1234567890
  },
  "memories": [
    { "kind": "error_resolution", "id": "mem_01J...", "title": "...", "learned": "...", "created_at": 1234567890 },
    { "kind": "user_rule",        "id": "mem_01J...", "title": "...", "learned": "...", "created_at": 1234567890 },
    { "kind": "task_pattern",     "id": "mem_01J...", "title": "...", "learned": "...", "created_at": 1234567890 }
  ]
}
```

### 8.4 `inspect` (Optional but Helpful)

A lightweight local inspection command is useful for debugging and trust.

Possible examples:

- list recent memories
- show one memory by ID
- print latest session summaries

This is optional for V1 but strongly recommended during development.

---

## 9. Development Milestones

| Phase | Deliverable | Success Criteria |
|---|---|---|
| **M1** | SQLite schema | `memories` table and `memories_fts` index created successfully |
| **M2** | `save` implementation | Structured memories save locally with validation |
| **M3** | Fingerprint dedupe | Exact duplicates are skipped reliably |
| **M4** | `search` implementation | Relevant FTS results returned with exact filters |
| **M5** | `context` implementation | Latest session summary + relevant memories returned in compact form |
| **M6** | Local CLI or stdio wrapper | Tool can be exercised end-to-end on a laptop |
| **M7** | End-to-end memory loop | A second run uses a first run's memories successfully |

---

## 10. Risks & Mitigations

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Agents save vague or useless memories | Medium | High | Enforce a strict schema and quality rules for `what` and `learned` |
| Duplicate memories flood the database | High | High | Add fingerprint-based dedupe from day one |
| Search returns noisy results | Medium | Medium | Keep memory fields small and structured; limit `context` payload size |
| Session summaries become repetitive clutter | Medium | Low | Only keep one summary per run and favor latest-per-task in `context` |
| FTS query behavior is confusing on short inputs | Medium | Medium | Prefer domain-specific queries and test common search phrases early |
| Schema is over-designed too early | Medium | Medium | Keep V1 fields minimal and defer optional metadata |

---

## 11. Out of Scope

The following are explicitly out of scope for droids-mem V1:

- deployment isolation and multitenancy
- HTTP service deployment concerns
- authentication and authorization layers
- cloud sync or cross-machine memory sharing
- vector search / embeddings
- web UI
- admin console
- memory editing workflows beyond basic duplicate suppression
- automated capture of all tool logs or model output

---

## 12. Glossary

| Term | Definition |
|---|---|
| **Memory** | A structured lesson saved for future agent runs |
| **FTS5** | SQLite's built-in full-text search engine |
| **Fingerprint** | A deterministic hash used to suppress duplicate memories |
| **`error_resolution`** | A memory describing a problem and the fix that worked |
| **`task_pattern`** | A repeatable successful pattern worth reusing |
| **`user_rule`** | A user correction or stable preference the agent should remember |
| **`session_summary`** | A run-level summary saved at the end of a task |
| **Context** | A compact set of memories returned at the start of a run |

---
