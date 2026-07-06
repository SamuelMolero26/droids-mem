# Changelog

All notable changes to droids-mem are documented here.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the
project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.1.1] — 2026-07-04

Headline: a native code graph so agents answer "what calls X" from a
pre-built index instead of grep, plus a retrieval and TUI pass.

### Added
- **Native code graph** (ADR-0020): a per-repo Go symbol + call-edge index
  (`go/packages` + `callgraph/cha`, interface dispatch resolved,
  over-approximate) under `~/.droids-mem/graphs/<hash>/`. Auto-rebuilds on repo
  change; a repo that stops type-checking serves the last good graph flagged
  `stale`. Shares nothing with the memory model — no scrub, no dedupe, never
  `mem.db`.
- `droids-mem graph` CLI: `index` (build/refresh), `symbol <name>` (source +
  callers/callees as signature stubs), `package <path>` (exported surface).
- **Two new MCP tools** — `graph_symbol`, `graph_package` — bringing the tool
  surface to six. Signatures-first: neighbors come back as one-line stubs,
  full body only for the exact qname asked.
- `graph_symbol` reports `transitive_callers` (blast size) on an exact match so
  a change's risk is visible before walking it; `direction=up depth>1` lists
  the blast radius, `to` gives the call path between two symbols. Bounded at
  500 (`blastCap`).
- `graph_symbol` search fallback: an unresolved `symbol` is treated as a task
  phrase and returns a relevance-ranked `matches` menu of signatures.
- **Write-time supersession** (ADR-0018): `supersedes=<id>` on save
  hard-deletes the target row in the same transaction.
- MCP server instructions for cross-host proactive integration (ADR-0019),
  plus agent-first friction fixes.

### Changed
- **FTS5 tokenizer wrapped with the porter stemmer** — folds morphological
  variants (`cancel` / `cancels` / `cancelling`) for better recall. Does not
  bridge true synonyms.
- **TUI redesigned** (phases 1 + 2 + refactor): a **CONNECTIONS** view showing
  how memories link to each other and to their source files. The stub Graph
  tab was dropped.
- Context bundle gained **modes** — `orient` (default, snippets) and `deep`
  (full bodies).
- Graph rebuilds skip test-file-only changes.

### Fixed
- MCP session-hook infinite-block loop and hook overuse (count-based staleness
  + `stop_hook_active` guard).

### Removed
- `internal/toon` (unused).

## [1.1.0] — 2026-06-18

Session memory: droids-mem now records a summary at the end of every Claude
Code session and replays relevant prior memories when related work starts —
via native hooks, no shell scripts or `jq`.

### Added
- **Native Claude Code session auto-summary** (ADR-0016). `droids-mem session
  hook` reads each hook's JSON on stdin and dispatches: `PostToolUse` (intake
  gate), `Stop` (record progress once enough work is unstaged), `SessionEnd`
  (flush staged summary), `SessionStart` (start bridge, recover crashed runs),
  `UserPromptSubmit` (inject relevant memories). Every hook fails open.
- `droids-mem install` wires the hooks into `~/.claude/settings.json`
  (`--project` targets `./.claude`, `--print` previews); `install --all` also
  starts the bridge, runs `claude mcp add`, and appends a CLAUDE.md block.
- `droids-mem tui`: interactive three-pane terminal browser (KINDS sidebar,
  list, detail) with live-search.
- `droids-mem recent-sessions`: list recent auto-saved session summaries.
- `droids-mem prune` (ADR-0010): manual delete by id + `--suggest-dupes`
  duplicate-cluster discovery. Retention is never automatic.
- Context bundle expand signal.

### Changed
- Module path lowercased to `github.com/samuelmolero26/droids-mem` for
  `go install` compatibility.

## [1.0.1] — 2026-06-09

### Fixed
- Corrected the module path so `go install` resolves the repository.

### Removed
- `CONTEXT.md` and `M0-decisions.md` from the repository.

## [1.0.0] — 2026-06-09

First public release. v1.0 ships the PII scrub pipeline, the `scope` column,
and a `PRAGMA user_version` migration mechanic on top of the existing
single-DB MCP server. Workspaces (ADR-0005) and git-JSONL sync (ADR-0006)
are deferred to v1.1 / v1.2.

### Added
- **PII scrub pipeline** (ADR-0007): 13 patterns redacted on save, single-pass
  merge with longer-wins / earlier-declaration tie-break. Pattern order:
  `pem_key` → `jwt` → `aws_key` → `github_token` → `stripe_key` → `slack_token`
  → `anthropic_key` → `openai_key` → `ssn` → `credit_card` (Luhn) → `phone`
  (E.164) → `private_ipv4` → `email`. Bracketed per-category redaction tokens
  (`[EMAIL]`, `[AWS_KEY]`, …).
- `scope TEXT DEFAULT 'shared' CHECK(scope IN ('personal','shared'))` column on
  `memories`. Optional on `mem_save`; forward-compat for the v1.1 workspace
  model.
- `scrub_pattern_version INTEGER DEFAULT 1` + nullable `scrub_counts TEXT` JSON
  column on `memories` for per-row scrub provenance.
- `meta(key, value)` table. `scrub_baseline_complete=1` sentinel gates the
  binary against pre-v1.0 plaintext databases.
- `droids-mem migrate --rescrub`: walks every row through the new scrub
  pipeline atomically, rewrites text fields, sets the sentinel.
- `droids-mem migrate --no-rescrub`: sets the sentinel without rewriting rows
  (escape hatch for already-trusted DBs).
- `droids-mem scrub --check <file>`: runs the pipeline against arbitrary text,
  prints the `ScrubReport`, no DB writes. `--test` runs the fixture corpus.
- `droids-mem doctor --scrub-stats`: aggregates `memories.scrub_counts` plus
  process-lifetime counters for `scrub_emptied_learned` and
  `tag_contains_secret` rejections.
- Field caps on save: `title=200`, `what=8192`, `learned=4096`, `tags=500`.
  Exceeding any cap returns `field_too_large`.
- Skip responses include `matched_title` + `matched_learned` so callers see
  exactly which row dedupe collided with.
- `scrub` block on save responses whenever `redaction_count > 0`
  (saved / skipped / updated, plus `scrub_emptied_learned` errors).
- `--version` flag on the root command.

### Changed
- **FTS5 tokenizer flipped** from `trigram` to `unicode61 tokenchars '_-'`.
  snake_case + kebab-case identifiers now index atomically.
  ~2-2.5× storage reduction on `memories_fts`. Migration drops + recreates the
  virtual table inside the `migrate --rescrub` transaction.
- Save-path normalization aligned with the new tokenizer: punctuation regex
  changes from `[^\w\s]` to `[^\w\s\-]` in `searchTerms`, `tokenSet`, and
  `normalizeForFP`. **Side effect**: fingerprints for existing rows change.
  `migrate --rescrub` re-fingerprints every row in the same transaction.
- `searchTerms` capped at 100 terms, sorted length-desc, to keep BM25 query
  construction bounded under caps-saturated 8 KB `what` fields.
- `fetchAllUserRulesConn` capped at 5 rows (older `user_rule` entries remain
  queryable via `mem_search kind=user_rule`).
- PRD §3.2 retuned to per-tier bundle targets: always tier = 1 last_session +
  ≤5 user_rules (full body); browse tier = ≤10 error_resolution + ≤10
  task_pattern (snippet). Replaces the old "≤10 items total" target.

### Fixed
- `mem_save --dry-run` no longer writes to the database. The full validate →
  scrub → dedupe → persist pipeline now runs inside a transaction that is
  always rolled back on dry runs.
- `mem_save` stopped echoing raw input back into the response payload on
  validation failure (sensitive content surfaced via error envelope only).
- Removed dead `DROP INDEX IF EXISTS idx_memories_task_kind` from the fresh
  DDL — was a no-op on every cold start since the composite index replaced it.

### Security
- MCP server binds to `127.0.0.1` by default; non-loopback binds emit a
  plaintext warning to stderr.
- `/identity?nonce=<n>` proves listener ownership of the bearer token via
  `HMAC-SHA256(token, nonce)`. `ensure-server` uses it to defend against port
  squatting.
- Tags strict-reject on scrub match: any tag containing a redacted pattern
  causes the save to fail with `tag_contains_secret` (`retryable:true`). No
  silent auto-strip.

### Migration notes
v1.0 refuses to boot against a pre-scrub database. Run either:

```
droids-mem migrate --rescrub      # rewrite every row through the scrub pipeline
droids-mem migrate --no-rescrub   # acknowledge plaintext, set the sentinel only
```

Both forms are atomic per DB. After either completes, the v1.0 binary boots
normally.

### Deferred
- **ADR-0005** (three-layer workspace model) → v1.1.
- **ADR-0006** (git-JSONL sync) → v1.2.
- `workspace.yml` / inline scrub config → v1.1. v1.0 pattern set + order are
  hardcoded.

[1.1.1]: https://github.com/SamuelMolero26/droids-mem/compare/v1.1.0...v1.1.1
[1.1.0]: https://github.com/SamuelMolero26/droids-mem/compare/v1.0.1...v1.1.0
[1.0.1]: https://github.com/SamuelMolero26/droids-mem/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/SamuelMolero26/droids-mem/releases/tag/v1.0.0
