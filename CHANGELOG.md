# Changelog

All notable changes to droids-mem are documented here.

Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the
project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **The code graph now covers TypeScript, TSX, JavaScript and Python, not just
  Go.** `graph_symbol` and `graph_package` answer for those languages through a
  new mapper tier built on tree-sitter, running alongside the Go tier rather
  than replacing it. The two never mix: no edge joins a Go symbol to a mapper
  one. What the new tier costs in precision is stated rather than hidden:
  - Every symbol response carries `precision` — `"resolved"` for a Go answer
    backed by the type checker, `"syntactic"` for a mapper answer resolved by
    name. A syntactic answer says so in its hint as well.
  - Mapper call edges come from a resolution ladder that narrows candidates by
    receiver arity, enclosing class, imported binding, defining file, then
    package. A rung that matches nothing falls through *without* narrowing, so
    a callsite can never resolve to zero candidates — the graph may report a
    caller that cannot fire, but never misses one that can.
  - A callsite whose repo-wide fallback exceeds the fan-out cap is labelled in
    `meta.fanout_capped` alongside its true pre-cap count, never silently
    truncated.
  - A mapper file that stops parsing cleanly carries its previous build's
    symbols and edges forward instead of dropping out of the graph, the same
    way a Go package that stops type-checking already did.
  - The `imports` table records module specifiers for the JS family and
    Python, each with its own explicit precision.
- **Code-graph answers now say how much to trust them.** Three signals, all
  response-level rather than repeated per row:
  - `callers_in_tests` splits the caller count, so "89 callers" reads as
    "5 production, 84 in tests" — the difference between a semantic change and
    a mechanical one, which a bare total hides.
  - `callers_via_interface` counts callers reached through interface dispatch.
    CHA over-approximates on purpose, so a method whose name is shared across
    many types (`Error`, `String`, `Close`) can report every function in the
    repo that calls *anything* of that name. That answer is now labelled
    instead of looking like a genuine hub.
  - `carried` on a symbol, and `stale_units` on freshness, name the packages
    whose edges came from the previous build rather than this one.
    `stale_units` is capped and rendered as `[N of M]`, like every other list
    on this surface. `carried` carries a hint, because the flag is narrower
    than it looks: the symbol, its signature and its callers are all freshly
    analyzed, and only its callees ride on the previous build. Without that,
    the safe reading is to distrust the whole answer and throw away the fresh
    caller list a blast-radius query was asking for.
- `freshness.stale` correspondingly narrows to mean a genuine build failure —
  a whole-graph fallback — instead of doubling as "some package didn't
  type-check". The MCP tool descriptions were updated to match; they had
  described the old behaviour.

### Fixed
- **A repo that stops type-checking no longer blanks the whole code graph.**
  Previously the first package with a type error aborted the entire build and
  every query fell back to the last good graph, marked stale — so one typo in
  one function body cost fresh answers for the whole tree. Now a broken package
  degrades alone: symbols are still extracted from source for every package
  (that needs no type information), fresh call edges are still computed for
  every package that compiles, and edges *out of* a broken package — the only
  ones that genuinely need a body we cannot type-check — are carried forward
  from the previous build and remapped by qualified name. Edges into a broken
  package survive natively. Carry-forward is best-effort: if the previous graph
  cannot be read the build still succeeds with nothing carried. If more than
  half the packages are broken the previous whole graph is served instead,
  since a mostly-carried answer is not worth the confusion.
- **The code graph now indexes test callers, and keeps its own reachability
  contract.** The graph documented that it "may report a caller that can't
  fire, never misses one that can", but `packages.Config` never set `Tests`, so
  no `_test.go` was loaded and **zero** indexed symbols came from a test file.
  `store.Store.Save` reported 5 callers while 84 distinct test functions call
  it — the blind spot largest exactly on exported API, which is what an agent
  queries before changing a signature. Three changes make it hold:
  - `Tests: true`, with package-variant deduplication. `packages.Load` returns
    several variants per tested package and the in-package test variant shares
    a `PkgPath` while re-parsing the same production files; without dedupe
    every production symbol was emitted twice, and because `symbols.qname` is
    not unique those duplicates inserted silently and made every symbol in a
    tested package report as ambiguous.
  - The staleness stamp's file census now includes `_test.go`, so editing a
    test file moves the stamp. Previously a newly added test caller was never
    indexed.
  - Caller lists order production callers before test callers. Test indexing
    roughly triples caller counts, and the previous ordering put a package's
    own test callers ahead of production callers in other packages — at the
    50-neighbour cap an agent could see zero production callers and wrongly
    conclude a signature change was test-only. Same-package proximity is kept
    as the secondary key. **Existing graphs rebuild once** on the first query
    after upgrading, because the file census changed.
- **`prune --suggest-dupes` now tokenizes like the save-time duplicate check it
  claims to mirror.** `dupeQuery` documents itself as building "the same capped,
  phrase-quoted OR query the save-time near-duplicate check uses", but the two
  had silently diverged: save-time runs on `dedupeTokens`, which replaces
  punctuation with a space, while `searchTerms` stripped it to nothing. So
  `store.Save` became the single token `storesave` in the prune query while
  FTS5's `unicode61` tokenizer had indexed it as `store` and `save` — a
  phrase-quoted term that could never match. `searchTerms` and `tokenSet` are
  now thin projections of the one `dedupeTokens` sweep, which is what the
  original fold intended.
- **The `phone` scrub detector no longer redacts numeric deltas** (issue #102).
  Its regex floor was two digits, so any signed number (`+370 bytes`, `+46%`,
  diff stats) matched the E.164 phone shape and was silently and irreversibly
  redacted before the row was stored — 6 of 7 matches were false positives.
  The floor is now the E.164 real-world minimum of seven digits. A 7+ digit
  numeric delta still matches; tightening further needs context rules and stays
  documented as a deliberate residual.
- **Code-graph cache now invalidates on a schema change.** `ensureFresh` gated
  only on the file-census stamp, and `graph.db` records no schema version, so
  editing the schema left every cached graph serving rows in the old shape
  until an unrelated source edit happened to move the stamp — a repo whose
  `.go` files were untouched kept the stale shape indefinitely. The stamp's
  generation is now derived from `sha256(schema + indexed extensions)` instead
  of a hand-written literal, so it cannot be forgotten. **Existing graphs
  rebuild once** on the first query after upgrading.
- **Non-Go repositories no longer split their cache.** `moduleRoot` anchored on
  `go.mod` only, so a tree without one had no anchor and naming a subdirectory
  keyed a second cache built from that subtree alone. It now falls back to the
  nearest ancestor `.git`, keeping `go.mod` first so a nested module still
  resolves to itself.
- **Build output is excluded from the source walk**: `dist`, `build`, `target`
  and `__pycache__` join the existing dotdir/`vendor`/`node_modules`
  exclusions, so a build no longer moves the staleness stamp.

## [1.2.1] — 2026-08-10

Headline: the MCP bridge stops trusting predictable secrets and loopback HTTP —
the bearer token now comes from crypto/rand and Claude Code connects over stdio
— plus a code-graph reliability pass, a retrieval-ranking pass, and a
fail-loud migration ladder.

### Security
- **MCP bearer token minted from crypto/rand** (issue #98). The token was
  `tok_` + oklog/ulid, whose default entropy is math/rand seeded with
  process-start nanotime — recoverable from the token file's own mtime by seed
  search, and the only barrier between another local account and the whole
  corpus. Now 32 bytes of crypto/rand, base64 RawURL (256 bits). Existing
  installs keep the old token until `~/.droids-mem/token` is deleted with the
  fixed binary on PATH.
- **Claude Code connects over stdio instead of loopback HTTP** (issue #98):
  `install --all` registers the server with `claude mcp add` using the stdio
  transport (`droids-mem serve --stdio`) — no port, no token file; the host
  owns the lifecycle, and the session-memory hooks stay in
  `~/.claude/settings.json`.
- **Code-graph cache hardened** (issue #98): the per-repo build lock can be
  abandoned when the waiting caller's context expires; a completed build lands
  on a 0600 temp file renamed into place (never world-readable); orphaned
  cache dirs (deleted worktrees, temp dirs) are swept best-effort, keeping any
  directory it cannot prove orphaned.

### Added
- **Code-graph status line**: `droids-mem statusline` prints
  `droids-mem:<tool>` when `graph_last` is under 60 s old, nothing otherwise —
  a Claude Code statusLine segment that makes code-graph use visible instead
  of silent. Both the MCP handlers and the `graph` CLI stamp it.
- **Migration ladder golden fixtures** (issue #97): per-version schema
  snapshots `schema_v0..v7.sql` under `internal/db/testdata/`, regenerated
  idempotently by `gen_fixtures.sh`; the parity test byte-compares migrated
  DBs against fresh ones (excluding FTS shadow tables) and asserts the porter
  tokenizer from the stored CREATE VIRTUAL TABLE text.

### Changed
- **Migration ladder restructured** (issue #97): rung 7→8 flips the FTS
  tokenizer from trigram to porter with a row-preserving reindex inside the
  rung transaction; rung 0→1 fixes the v0→v1 `scope` default to match the
  fresh schema (`personal`). `store.Migrate` shrinks to precondition + optional
  rescrub + sentinel. **`migrate --rescrub` now fails loudly (exit 5)** when
  two rows converge to the same fingerprint — a `FingerprintCollisionError`
  names both rows instead of silently dropping one.
- **TUI: code-graph tab removed** (`ctrl+g`). The graph browser duplicated the
  `graph` CLI and the `graph_symbol` / `graph_package` MCP tools, and it was
  the only reason the TUI depended on `internal/graph`. Graph use now surfaces
  through the status line instead.
- **`mem_search` ranks by the project's bm25 column weights** (issue #82):
  retrieval now uses the same column weights as dedupe (`title` 3 / `learned`
  2 / `what` 1 / `tags` 1) instead of flat ranking; the no-query browse tier
  ranks by recency instead of `task_type` MATCH (issues #76/#79); overlap
  weights and the recall floor were retuned against the benchmark (issue #81).
- **Newest-N ordering tiebroken on `id DESC`** (issues #58/#85): rows sharing
  a timestamp no longer order arbitrarily.
- **CI hardened** (issue #87): every third-party action pinned to a full
  40-char commit SHA, dependency-review job added, top-level read-only
  permissions with per-job grants, and release-gated workflows that validate
  the tag (`vMAJOR.MINOR.PATCH`, tag must be an ancestor of main, and
  CHANGELOG.md must carry the version section). Also adds SECURITY.md, a
  dependabot config, and a PR template.

### Fixed
- **Code-graph async rebuild lifecycle** (issue #73). Six defects on the
  warm-serve path, found by exercising it end to end:
  - A completing rebuild closed the SQLite handle its caller was still
    querying, so a build landing mid-response surfaced as
    `sql: database is closed` instead of the intended degraded stale answer.
    Handles are refcounted now and close only when the last holder releases.
  - A superseded build's completion channel was never closed, so
    `graph_build_wait` waited out its full timeout for a build whose result had
    already been discarded.
  - `graph_build_wait` ignored its `timeout` when no rebuild was in flight: it
    started one and immediately reported `completed: true`, and on a
    never-indexed repo it ran a full synchronous build regardless of the
    timeout. The timeout now bounds every path, expiry reports
    `completed: false` rather than an error, and waiting attaches to the build
    it triggered.
  - A repo that stopped type-checking relaunched a full `go/packages` load on
    *every* query. The retry is now suppressed while the source still hashes to
    a stamp that already failed; any edit lifts it. This covers the first-ever
    index too: a repo that has never been indexed *and* does not type-check
    reports the recorded reason instead of rebuilding on every query, whether or
    not the caller that triggered the failing build stayed to see it.
  - The per-repo build lock ignored the waiting caller's context. Since an
    abandoned cold build keeps that lock until it lands, a second caller blocked
    for the whole build no matter what deadline it asked for — `graph_build_wait`
    included. Acquiring the lock can now be abandoned when the caller's context
    expires; taking an uncontended lock still never fails.
  - The `graph_build_wait` timeout response reported an empty stamp, hiding the
    valid stamp of the graph still being served.
  - A first-ever index ran on the caller's context, so a client disconnecting
    mid-build discarded all the work and the next query restarted from zero.
    The build now survives an abandoned caller while the caller's own wait stays
    bounded.
- **CLI and PATH-less servers can refresh a stale graph** (issue #95): the
  `graph` CLI and a server started with no `go` on PATH can now refresh a
  stale index through the Go toolchain resolver.
- **Ghost callers dropped** (issues #69/#88): a call made *from* inside a
  closure is attributed to the enclosing declaration; a call *into* a closure
  is no longer reported as a caller of the function it was passed to.

## [1.2.0] — 2026-07-22

### Changed
- **README rewritten** as a landing page — problem-first structure, no ASCII
  art, concise sections for quick scan, clearer positioning for new users.
  Adds a "For whom?" section, a "Contributing" call-to-action, and a compact
  CLI reference table.

### Added
- **Opt-in shared context** (ADR-0028 / ADR-0029): memories carry a
  `scope` (`personal` | `shared`); `scope` now defaults to `personal` (v4→v5
  migration backfills every existing row) so nothing leaves the local store
  implicitly. Sharing is driven from the **TUI**: `s` cycles a scope filter,
  `^s` opens a confirm dialog that flips the selection into a **git-tracked
  shared pool** (`shared.jsonl`) and pushes, `^p` pulls a teammate's pool, `^x`
  unshares a row back to personal. Every shared copy is scrubbed at the trust
  boundary; import dedupes across sources by fingerprint + Jaccard. Store
  primitives (`ExportShared` / `ImportShared` / `CountShared` / `SetScope`) back
  it; no CLI `share`/`export`/`import` verbs.
- **Uninstall command** (issue #27): `droids-mem uninstall` mirrors `install` —
  removes the session-memory hooks from `~/.claude/settings.json`, deregisters
  the MCP bridge, and strips the CLAUDE.md guidance block. Idempotent and
  non-destructive to unrelated config; `--project`, `--host codex|opencode`,
  and `--print` mirror `install`.
- **TOON on the code-graph surface** (ADR-0027): `graph_symbol` / `graph_package`
  (MCP + CLI) render their signatures-first neighbor arrays as TOON
  (Token-Oriented Object Notation) — one shared header per array plus bare
  rows, dropping the per-row JSON key repetition on the tabular graph output.
- **`implements` edges on `graph_symbol`** (issue #48): an exact-match symbol
  surfaces its interface↔concrete `implements` relationships alongside
  callers/callees.
- **Threshold-tuning log** (ADR-0026): an env-gated append log that records the
  two unvalidated retrieval thresholds' live score distributions
  (`jaccardDupeThreshold` near-dupe gate, `DefaultRelevanceFloor` recall floor)
  so they can be retuned from data instead of inspection. Off by default.
- **Multi-host install** (ADR-0019): `droids-mem install --host codex|opencode`
  registers droids-mem as a stdio MCP server in that host's config
  (`~/.codex/config.toml` / `~/.config/opencode/opencode.json`). Idempotent and
  non-destructive; `--print` shows the snippet without writing. `--all` stays
  Claude-only.
- **Stdio transport**: `droids-mem serve --stdio` serves MCP over stdin/stdout
  for hosts that spawn the server as a child process — no port, token, or
  `ensure-server`; the host owns the lifecycle. `--addr`/`--endpoint` are
  ignored. On stdio hosts the server instructions tell the model to save its
  own end-of-run `session_summary` (no summary hook is wired); dedupe keeps
  that safe if a hook is added later.
- **Recall benchmark** (ADR-0025): a fixed 24-memory / 33-paraphrase corpus in
  `internal/store/recall_benchmark_test.go` that scores retrieval across the
  vocabulary gap and fails the build on regression; report in
  [`eval/RESULTS.md`](eval/RESULTS.md), summary in the README.

### Changed
- **Graph neighbor truncation ranks by same-package first** (issue #49): when a
  hub symbol's callers/callees exceed the cap, the kept set favours
  same-package neighbors, and the response reports honest totals so a
  truncated list is visible as truncated.
- **`graph_symbol` per-tool adoption counter** (issue #51): a byte-append
  side-file (0600) tallies how often each graph tool is invoked, for adoption
  telemetry without touching `mem.db`.
- **`transitive_callers` omitted on non-callable symbols** (issue #47): the
  blast-size field only appears where a call graph exists, not on types/vars.
- **Boot gate auto-remediates** (issue #29): a stale scrub baseline now
  auto-runs `migrate --rescrub` on the first non-bypassed command instead of
  taking down all memory tools until a manual migrate. The `boot_gate` error
  only surfaces if the auto-migration itself fails.
- **MCP runtime errors return a structured envelope** (`status`, `error`,
  `message`, `retryable`, `suggestion`) — the dominant case is a transient
  `BEGIN IMMEDIATE` write-lock timeout under concurrent writers, so the agent
  sees `retryable` instead of a raw `SQLITE_BUSY` string.

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

[Unreleased]: https://github.com/SamuelMolero26/droids-mem/compare/v1.2.1...HEAD
[1.2.1]: https://github.com/SamuelMolero26/droids-mem/compare/v1.2.0...v1.2.1
[1.2.0]: https://github.com/SamuelMolero26/droids-mem/compare/v1.1.1...v1.2.0
[1.1.1]: https://github.com/SamuelMolero26/droids-mem/compare/v1.1.0...v1.1.1
[1.1.0]: https://github.com/SamuelMolero26/droids-mem/compare/v1.0.1...v1.1.0
[1.0.1]: https://github.com/SamuelMolero26/droids-mem/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/SamuelMolero26/droids-mem/releases/tag/v1.0.0
