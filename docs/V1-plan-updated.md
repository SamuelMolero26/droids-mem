# droids-mem SQLite+FTS5 Hardening Plan

## Context

The droids-mem V1 backbone is mostly implemented but a SQLite+FTS5 audit (engram observation `discovery/droids-mem-sqlite-fts5-audit-findings`) surfaced a critical concurrency race, weak near-duplicate detection, a tokenizer that fails on jargon, and several gaps around FTS sync integrity, context bundle UX, and operational hygiene. This plan resolves all flagged issues before declaring V1 stable. Outcome: race-free saves, robust dedupe across corpus growth, agent-friendly substring search, predictable context bundles, and a recovery path for FTS divergence.

## Decisions locked (this session)

| # | Topic | Decision |
|---|-------|----------|
| 1 | BM25 dupe | Cosine/Jaccard on top-20 BM25 candidates, threshold ~0.85 |
| 2 | Tokenizer | Switch FTS5 to `tokenize="trigram"` |
| 3 | Save race | `BEGIN IMMEDIATE` txn wrap + `ON CONFLICT(fingerprint) DO UPDATE` for force path |
| 4 | FTS sync | Code audit for `INSERT OR REPLACE`, periodic `optimize`, new `doctor` subcommand |
| 5 | Bundle UX | Drop `--limit`. Always-tier (latest summary + all user_rules, full body) + browse-tier (top errors/patterns as title+1-liner). Agent uses `search`/`get` for depth |
| 6 | Fingerprint | Keep current (exclude `what`). Document rationale in ADR |
| 7 | PII | Add `scrubPII(string) string` pass-through hook in `validate()`. Patterns added in future PR |

## Implementation

### 1. Schema + FTS hardening (`internal/db/schema.go`)
- Change `memories_fts` to `tokenize="trigram"`. Drop default `unicode61`.
- Keep `content='memories' content_rowid='rowid'` external-content pattern.
- Verify all 3 triggers (INSERT/UPDATE/DELETE) still correct with trigram tokenizer.
- Grep codebase for `INSERT OR REPLACE`, `REPLACE INTO`, any rowid reassignment. Forbid in `memories` table. Add comment in schema.go.

### 2. Save path (`internal/store/save.go`)
- Wrap `findByFingerprint` + BM25/cosine check + INSERT/UPDATE in single `BEGIN IMMEDIATE` transaction.
- Replace force-path branching with `INSERT ... ON CONFLICT(fingerprint) DO UPDATE SET title=excluded.title, what=excluded.what, learned=excluded.learned, tags=excluded.tags, updated_at=...`.
- Replace `bm25Check` near-duplicate logic:
  - FTS query: top-20 candidates by `bm25(memories_fts, 3, 1, 2, 1)` ranking.
  - Compute Jaccard similarity over normalized token sets of (title+what+learned+tags).
  - Threshold: `>=0.85` → near-duplicate, skip (or force-update).
  - Remove `bm25DupeThreshold = -15.0` constant.
- Move `pruneSessionSummaries` call INSIDE the save txn so prune is atomic with insert.
- Lowercase-normalize `task_type` in `validate()`.
- Add `scrubPII(s string) string` stub in new file `internal/store/scrub.go`. Returns input unchanged. Call from `validate()` on `title`, `what`, `learned`, `tags` BEFORE fingerprint computation.

### 3. DB connection (`internal/db/db.go`)
- Add `PRAGMA busy_timeout=5000;` after `journal_mode=WAL`.
- Keep `synchronous=NORMAL`, `foreign_keys=ON`.

### 4. Context bundle rework (`internal/store/context.go`, `cmd/droids-mem/cmd_context.go`)
- Remove `--limit` flag from `cmd_context.go`.
- Remove `slotErrorResolution=3`, `slotUserRule=2`, `slotTaskPattern=2` constants.
- New behavior:
  - **Always-tier** (full body): latest 1 `session_summary` + ALL `user_rule` rows for the `task_type`.
  - **Browse-tier** (title + first 120 chars of `what`): top 10 `error_resolution` + top 10 `task_pattern` by BM25 of `--query` (or tokenized `task_type` if no query).
- Response shape gains `tier: "always" | "browse"` field per memory so agent knows whether to call `get --id` for full body.
- Update `internal/store/context_test.go` for new shape.
- Update `droids-mem schema context` output.

### 5. Search count fix (`internal/store/search.go`)
- Run second query `SELECT count(*) FROM memories_fts WHERE memories_fts MATCH ?` before the LIMIT query.
- Set `Total` to actual match count (pre-limit). Agent can paginate accurately.

### 6. Doctor subcommand (new `cmd/droids-mem/cmd_doctor.go`)
- `droids-mem doctor` runs:
  - `INSERT INTO memories_fts(memories_fts) VALUES('integrity-check')` — surfaces FTS/base divergence.
  - If divergent: `INSERT INTO memories_fts(memories_fts, rank) VALUES('rebuild', ...)`.
  - `INSERT INTO memories_fts(memories_fts) VALUES('optimize')` — compact index.
  - `VACUUM` — reclaim DB file space.
- JSON output: `{status, integrity_ok, rebuilt, optimized, vacuumed, bytes_freed}`.
- Register in `cmd/droids-mem/main.go`.

### 7. Docs

- **New ADR**: `droids-mem/docs/adr/0001-fingerprint-excludes-what.md` — explain rationale (lesson identity = title+learned; `what` is interchangeable framing; Layer 2 cosine catches genuinely different lessons).
- **New ADR**: `droids-mem/docs/adr/0002-context-bundle-tier-model.md` — explain drop of `--limit`, always/browse/lazy tiers.
- **Update M0-decisions.md §6** (BM25 threshold): replace with cosine/Jaccard approach.
- **Update M0-decisions.md §1** (CLI surface): add `doctor` subcommand, remove `--limit` from `context`.
- **Update CONTEXT.md**: revise "Context bundle" glossary entry to describe tier model instead of slot count.
- **Append to droids-mem/Future.md**:
  - Lazy loading patterns refinement (agent-driven `get`/`search` chains).
  - Mode presets (`--mode orient|deep|refresh`) layered on top of tier model.
  - Auto-compaction of repetitive user_rules into superseding entries.
  - PII regex patterns (email/phone/API keys/JWT/credit cards).

## Critical files to modify

| File | Change |
|------|--------|
| `droids-mem/internal/db/schema.go` | trigram tokenizer, comment forbidding REPLACE |
| `droids-mem/internal/db/db.go` | busy_timeout pragma |
| `droids-mem/internal/store/save.go` | txn wrap, UPSERT, Jaccard dedupe, prune in txn, task_type normalize |
| `droids-mem/internal/store/scrub.go` | NEW: `scrubPII` stub |
| `droids-mem/internal/store/search.go` | Total = real count via second query |
| `droids-mem/internal/store/context.go` | tier model, drop slot constants |
| `droids-mem/cmd/droids-mem/cmd_context.go` | drop `--limit`, new response shape |
| `droids-mem/cmd/droids-mem/cmd_doctor.go` | NEW: doctor subcommand |
| `droids-mem/cmd/droids-mem/main.go` | register doctor cmd |
| `droids-mem/cmd/droids-mem/cmd_schema.go` | reflect context shape change, add doctor |
| `droids-mem/internal/store/*_test.go` | update tests for new behavior |
| `droids-mem/cmd/droids-mem/e2e_test.go` | add concurrency race test, doctor cmd test |
| `droids-mem/M0-decisions.md` | update §1 and §6 |
| `droids-mem/CONTEXT.md` | revise Context bundle entry |
| `droids-mem/Future.md` | append modes/lazy/compaction/PII items |
| `droids-mem/docs/adr/0001-fingerprint-excludes-what.md` | NEW |
| `droids-mem/docs/adr/0002-context-bundle-tier-model.md` | NEW |

## Verification

### Unit tests
- `save_test.go`: add concurrent save test — spawn 10 goroutines saving identical memory, expect exactly 1 row, no UNIQUE constraint error surfaced.
- `save_test.go`: Jaccard threshold test — known near-dupe pairs flagged, distinct memories not.
- `context_test.go`: bundle returns always-tier with full body + browse-tier with truncated `what`.
- `search_test.go`: Total reflects full match count not page size.
- `schema_test.go`: trigram tokenizer matches substring queries (`bspot` matches `hubspot`).

### End-to-end
- `e2e_test.go`: existing two-run test passes against new tier shape.
- New: doctor cmd e2e — corrupt FTS by direct write to `memories_fts` table, run doctor, verify integrity restored.
- New: concurrent CLI race — fork 5 `droids-mem save` processes with same memory, verify single row + clean exit codes.

### Manual smoke
1. `go build ./cmd/droids-mem && ./droids-mem schema` — confirm `context` lists new response shape, `doctor` appears.
2. `./droids-mem save --task-type test --kind error_resolution --title "HubSpotPhone bug" --what "..." --learned "..."` then `./droids-mem search --query "spot"` — trigram substring hit confirms tokenizer.
3. `./droids-mem context --task-type test --query "phone"` — confirm always-tier full bodies + browse-tier truncated, no `--limit` flag accepted.
4. `./droids-mem doctor` — confirm JSON output with integrity_ok=true on healthy DB.

## Out of scope

- True PII regex patterns (stub only; future PR).
- Mode presets (`--mode orient|deep`) — documented in Future.md.
- Auto-compaction of related memories — Future.md.
- TTL/retention beyond existing 5-summary cap — Future.md.
- TOON serialization for context output — already in Future.md.
