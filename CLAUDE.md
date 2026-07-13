# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build / test / run

All commands run from this directory (`droids-mem/`):

```
go build ./cmd/droids-mem           # single binary: CLI + serve + ensure-server
go run ./cmd/droids-mem <subcmd>    # run without building
go test ./...                       # all tests
go test -count=1 ./...              # bypass test cache
go test ./internal/store -run TestSave_DedupesByFingerprint  # single test
```

Frictionless startup (no env required):

```
droids-mem ensure-server   # ping /healthz, auto-spawn serve detached if down
droids-mem serve           # foreground MCP bridge
```

E2E suites live in `cmd/droids-mem/`:
- `e2e_test.go` — CLI end-to-end via built binary.
- `serve_e2e_test.go` — drives `droids-mem serve` over JSON-RPC on ephemeral port; covers auth, tool surface, session minting, dedupe, SIGTERM.

Both suites isolate `DROIDS_MEM_DB` and `DROIDS_MEM_HOME` per test.

## Runtime env

| Var | Default | Notes |
|-----|---------|-------|
| `DROIDS_MEM_DB` | `~/.droids-mem/mem.db` | Always override in tests |
| `DROIDS_MEM_HOME` | `~/.droids-mem/` | token, pid, log files |
| `DROIDS_MEM_MCP_TOKEN` | auto (see state pkg) | Bearer token for `/mcp` |
| `DROIDS_MEM_MCP_ADDR` | `127.0.0.1:7777` | Bind address (loopback by default; non-loopback logs a plaintext warning) |
| `DROIDS_MEM_MCP_ENDPOINT` | `/mcp` | `/healthz` + `/identity` always unauthenticated |

State dir layout: `mem.db` (0600), `token` (0600), `mcp.pid`, `mcp.log`.

`/identity?nonce=<n>` answers `HMAC-SHA256(token, nonce)` — ensure-server uses it
to verify a listener actually holds the token before reporting `already_running`
(anti port-squatting).

## Architecture

Single binary, layered. Don't bypass layers:

1. **`cmd/droids-mem/`** — cobra subcommands. One `cmd_*.go` per command; delegates to store, emits JSON via `output.go`. No business logic.
2. **`internal/mcpserver/`** — MCP bridge (`server.go` wires HTTP + auth, `stdio.go` the stdio transport for host-spawned servers (`serve --stdio`, ADR-0024 — no port/token; instructions string forks one summary sentence per transport), `tools.go` defines the 4 memory tools, `graph_tools.go` the 2 code-graph tools). Operator commands (`list`, `schema`, `doctor`, `prune`) intentionally not exposed here.
3. **`internal/store/`** — all business logic shared by CLI and MCP. Key files:
   - `save.go` — validate → scrub → fingerprint → dedupe (2 layers) → insert; owns scrub *policy* (which fields, tag + identifier strict-reject, empty-after-scrub)
   - `search.go` — FTS5 MATCH queries
   - `context.go` — two-tier context bundle assembly (always + browse)
   - `doctor.go` / `inspect.go` — health checks (incl. ADR-0010 growth warnings), introspection
   - `prune.go` — manual deletion + `--suggest-dupes` cluster discovery (ADR-0010); never automatic
   - `scrub.go` — thin aliases re-exporting the engine from `internal/scrub`
   - `eval.go` — recall eval engine (ADR-0025): scores paraphrase→memory fixture pairs against `mem_search`/`mem_context`; driven by `recall_benchmark_test.go`
4. **`internal/scrub/`** — the scrub *engine* (ADR-0008): `spec.yaml` (embedded declarative detector spec, single source of truth, pinned-hash version enforcement), `scrub.go` (single-pass collect → overlap-resolve → splice, windowed scanning), `entropy.go` (deterministic gate for usage-class detectors), `corpus.go` + `testdata/` (fixture corpus, `[CUT]` defang convention). No store imports.
5. **`internal/db/`** — `db.go` opens connection + applies pragmas; `schema.go` holds raw DDL string.
6. **`internal/state/`** — `LoadOrCreateToken()` is the canonical bearer-token resolver. Owns all `~/.droids-mem/` file ops.
7. **`internal/graph/`** — native code-graph subsystem (ADR-0020): per-repo Go symbol/call-edge index under `~/.droids-mem/graphs/<hash>/graph.db`, built with `go/packages` + `callgraph/cha` (interface dispatch resolved, over-approximate). Staleness-check-on-query via a `.go` count/size/max-mtime stamp; a repo that stops type-checking serves the last good graph with `stale: true`. Shares NOTHING with the Memory model — no scrub, no dedupe, no retention, never mem.db. Consumed by `cmd_graph.go` (boot-gate bypassed — annotations don't inherit, each leaf carries the bypass) and `internal/mcpserver/graph_tools.go`.

## Data model invariants

- `memories` is source of truth. `memories_fts` for `MATCH` + `rank` only — never filter or join on it.
- FTS sync via 3 triggers (AI/AD/AU). Direct inserts to `memories_fts` are bugs.
- `tags` — space-delimited string, NOT JSON (FTS5 tokenizes on whitespace).
- `updated_at = created_at` set in code on insert. CHECK constraint enforces `updated_at >= created_at` at DB layer. Never `DEFAULT 0`.

## Dedupe (save)

Two layers, both must pass before insert:
1. **Fingerprint** — SHA-256 of normalized (`title+learned` + `task_type` + `kind`). Excludes `what` by design (ADR 0001). Exact match → skip (or overwrite if `force=true`).
2. **Near-duplicate** — BM25 top-20 candidates (on `title+what+learned+tags`, column weights `bm25(memories_fts, 3, 1, 2, 1)`) re-ranked by Jaccard token-set similarity. Threshold: `≥ 0.85` → near-duplicate → skip. `SaveResponse` includes `score` (Jaccard) and `matched_id` when skipped.

Both layers run inside a `BEGIN IMMEDIATE` transaction to close the dedupe race.

## Context bundle

Two-tier model. No `--limit` flag; tier sizes are hardcoded constants.

**Always tier** (full `learned` body):
- `last_session` — 1× latest `session_summary` for `task_type` (optional)
- `user_rules[]` — newest 5 `user_rule` rows for `task_type` (decision #20)

**Browse tier** (title + 120-rune snippet from `what`):
- rule stubs — `user_rule` rows beyond the always-tier 5, title-only, listed first (ADR-0011)
- ≤10 `error_resolution` by BM25 rank
- ≤10 `task_pattern` by BM25 rank

Response shape: `{ task_type, last_session?, user_rules[], user_rules_total, browse[] }`. Each item has `tier: "always"|"browse"`. All reads are wrapped in `BEGIN DEFERRED` for a consistent snapshot.

Session retention: on `session_summary` save, delete oldest if > 5 for that `task_type`.

## CLI contract

- All output: JSON to stdout. Errors: JSON to stderr.
- Exit codes: `0` ok, `1` runtime, `2` usage, `3` not found, `5` conflict/duplicate, `10` dry-run pass.
- All flags long-form. No short aliases in V1.
- `--dry-run` on `save` → structured JSON + exit `10`.
- Error envelope: `{status, code, field?, message, input?, retryable, suggestion}`.

## MCP contract

6 tools: `mem_save`, `mem_search`, `mem_context`, `mem_get` (memory) + `graph_symbol`, `graph_package` (code graph, ADR-0020 — signatures-first, agent passes `repo` = absolute project root).

- `mem_context` mints `session_id` (stateless server — agent stores and reuses it).
- Auth: `Authorization: Bearer <token>` on every `/mcp` request. Stdio transport (`serve --stdio`, ADR-0019) has no port/token — the pipe is private to the spawning host; same tool surface, only the instructions string's summary sentence differs (stdio hosts self-save a `session_summary`).
- `*store.ValidationError` → MCP tool error `{error, field, message}`; other runtime errors → structured envelope `{status, error, message, retryable, suggestion}` (dominant case: transient `BEGIN IMMEDIATE` write-lock timeout, ADR-0024).
- SIGTERM → `http.Server.Shutdown` (10 s grace) → `db.Close`.

## Consumer pattern (ADR 0004)

Only Root agent writes to `droids-mem`. Sub-agents get no MCP tools — they consume the context Bundle injected by Root. Root runs `mem_context` first, threads `session_id` through the run, then fans out `mem_save` calls in Rollup. The 4-kind enum (`session_summary`, `task_pattern`, `error_resolution`, `user_rule`) is frozen — no `observation` kind.

## Dependencies (locked)

- `modernc.org/sqlite` — pure Go, FTS5, no CGO. Do not swap for `mattn/go-sqlite3`.
- `github.com/oklog/ulid/v2` — IDs.
- `github.com/spf13/cobra` — CLI.
- `github.com/mark3labs/mcp-go` — MCP SDK. Used only by `internal/mcpserver`.

## Reference docs

- `files/Droids-mem-PRD.md` — full product spec, data model, response shapes.
- `M0-decisions.md` — locked pre-impl decisions. Read before changing any design assumption.
- `files/CLI-GUIDE.md` + `files/CHECKLIST.md` — CLI design rules.
- `CONTEXT.md` — domain language and term aliases.
- `docs/adr/0001` — fingerprint scope decisions.
- `docs/adr/0002` — context bundle tier model.
- `docs/adr/0003` — MCP transport, bearer auth, session ownership.
- `docs/adr/0004` — parent-as-memory-broker pattern (why sub-agents don't write to droids-mem).
- `docs/adr/0005` — three-layer workspace model.
- `docs/adr/0006` — git JSONL sync for project workspaces.
- `docs/adr/0007` — PII scrub pipeline.
- `docs/adr/0008` — layered scrub detectors.
- `docs/adr/0009` — store owns error serialization.
- `docs/adr/0010` — no automatic retention; doctor warnings + manual prune with dupe-cluster suggestions.
- `docs/adr/0011` — user_rule overflow surfaces as browse-tier stubs + `user_rules_total`.
- `docs/adr/0020` — native code graph (Go-only, per-repo graph.db, signatures-first tools).
- `Future.md` — deferred / post-V1 ideas.

## Engineering practices

- We're a startup. You're probably used to writing enterprise code - code that tries to handle every possible edge case and has fallbacks for everything. That's not how we do things around here: our number one rule is to keep things simple. We handle ONLY the most important cases.
- We try to only add new functionality that is small (that is, simple and few lines of code) or absolutely necessary. If a change is not small or absolutely necessary, don't make it.

- Use cc-skills-golang for best go practices
