![droids-mem Logo](assets/droids-mem-logo.png)

# droids-mem

**An AI agent's memory, stored in a single SQLite file, zero external services.**

Every time your agent learns something — a fix that worked, a project convention,
a hard-won error resolution — droids-mem keeps it. Next session, that lesson is
there when it's needed, retrieved by meaning not keywords.

One binary. One database. One `brew install`. No vector DB, no API key, no
third-party service.

---

## For whom?

You use an AI coding agent (Claude Code, Codex, OpenCode, Cursor, any MCP host)
and you're tired of:

- Re-explaining the same project conventions every session.
- Watching your agent repeat mistakes it fixed last week.
- Wiring up vector databases, embedding pipelines, and API keys just to give an
  agent continuity.

**droids-mem is local-first persistent memory that works out of the box.** Your
agent writes lessons; it reads the relevant ones at the start of the next run.
That's it.

---

## Quick start

```shell
# macOS / Linux
brew tap samuelmolero26/tap
brew install droids-mem

# Save your first lesson
droids-mem save \
  --task-type "my-project" \
  --kind error_resolution \
  --title "FTS5 deadlocks on trigger rebuild in same txn" \
  --what "ALTER TABLE inside a transaction, then rebuilding memories_fts in the same txn causes SQLITE_BUSY" \
  --learned "DROP the FTS table and INSERT-SELECT must run AFTER the parent ALTER commits"

# Search it later, even with different words
droids-mem search --query "fts trigger rebuild" --limit 5

# Browse everything interactively
droids-mem tui
```

All output is JSON on stdout; errors are JSON on stderr. Exit codes: `0` ok,
`1` runtime, `2` usage, `3` not found, `5` conflict/duplicate, `10` dry-run.

### Wire it to your agent

```shell
# One-shot: hooks, MCP bridge, and CLAUDE.md block
droids-mem install --all

# Or per host
droids-mem install --host opencode
droids-mem install --host codex
```

Done. Your agent now has persistent memory.

---

## What it does

### Memory

Agents save structured lessons — session summaries, task patterns, error
resolutions, user rules. On save, droids-mem:

1. **Scrubs secrets** — API keys, tokens, credentials, PII. Detects 15+
   patterns (AWS/GitHub/Stripe/OpenAI keys, JWTs, PEMs, emails, phones, private
   IPs). Rejects tags that match; redacts text fields.
2. **Deduplicates** — SHA-256 fingerprint on content + near-duplicate detection
   via BM25 + Jaccard similarity (≥ 0.85 threshold). Saving the same lesson
   twice is harmless.
3. **Stores** — one row in a local SQLite file.

On load, droids-mem assembles a **two-tier context bundle** for the agent:
always-tier (last session summary + standing user rules, full body) and
browse-tier (relevant error resolutions and task patterns, ranked by BM25).

### Code graph (Go repos)

For Go projects, droids-mem builds a per-repo index of symbols and call edges
(interface dispatch resolved, over-approximate). Instead of grep to find "what
calls X", you get:

```shell
droids-mem graph symbol Store.Save --repo /path/to/project --direction up --depth 3
# → source + callers as signature stubs + transitive_callers count
```

Pre-built, signatures-first, agent-cheap. Auto-rebuilds on repo change; a repo
that stops type-checking serves the last good graph flagged `stale`.

### TUI

A three-pane terminal browser over the corpus — **KINDS** sidebar, a memory
list, and a detail pane. Type to live-search (≥ 3 chars), `tab` cycles focus,
`ctrl+d` deletes with confirmation. A **CONNECTIONS** view surfaces links
between memories and their source files.

```shell
droids-mem tui
```

![droids-mem TUI](assets/tui.png)

### Session memory (Claude Code)

droids-mem hooks into Claude Code's lifecycle natively:

| Event | What happens |
|---|---|
| `SessionStart` | Starts the MCP bridge; recovers crashed-run summaries |
| `UserPromptSubmit` | Injects relevant prior memories for the prompt |
| `PostToolUse` | Counts meaningful work (intake gate) |
| `Stop` | Once enough is unstaged, asks the model to record progress |
| `SessionEnd` | Saves the staged summary if the gate passes |

Every hook fails open — a memory hiccup never breaks your session. Full
reference: [`hooks/README.md`](hooks/README.md).

---

## Retrieval performance

The core bet: an agent should find a lesson later even when it's phrased
*differently* than it was saved. This is measured, not asserted.

A fixed benchmark of **24 memories** in seven distractor clusters (retrieval has
to beat confusable neighbours) is queried by **33 hand-authored paraphrases**
whose wording is independent of the target. Runs in CI
(`internal/store/recall_benchmark_test.go`); full report in
[`eval/RESULTS.md`](eval/RESULTS.md).

| Query class | recall@1 | recall@5 |
|---|---|---|
| Word-order / partial reword | 100% | 100% |
| Morphological ("cancel" → "cancelling") | 100% | 100% |
| Synonym, **zero shared words** | 67% | 75% |
| **Overall** | **88%** | **91%** |

This is FTS5 + porter stemming — **no embeddings, no vector DB**. Pure-Go, no
CGO. The honest ceiling is synonym substitution: a query that shares zero words
with the memory (e.g. "too many requests" → a lesson titled "HTTP 429") can
only be bridged by luck. Those misses are documented by name in the full report.

Reproduce:

```shell
go test ./internal/store -run TestRecallBenchmark -v
```

---

## Install

### Homebrew (macOS / Linux)

```shell
brew tap samuelmolero26/tap
brew install droids-mem
```

If Homebrew refuses the tap as untrusted: `brew trust samuelmolero26/tap`.

### Go toolchain

```shell
go install github.com/samuelmolero26/droids-mem/cmd/droids-mem@latest
```

Requires Go 1.25+. Pure-Go (`modernc.org/sqlite`) — builds without CGO.

### Prebuilt binary

Grab one for `linux/{amd64,arm64}` or `darwin/{amd64,arm64}` from the
[Releases page](https://github.com/SamuelMolero26/droids-mem/releases).

### From source

```shell
git clone https://github.com/SamuelMolero26/droids-mem
cd droids-mem && go build ./cmd/droids-mem
./droids-mem --version
```

---

## MCP tools

Six tools over the MCP bridge (bearer auth, or stdio for host-spawned servers):

**Memory**
- `mem_save` — persist a lesson (scrubs + dedupes).
- `mem_search` — full-text search (BM25 ranked).
- `mem_context` — two-tier context bundle for a `task_type`; mints a `session_id`.
- `mem_get` — fetch one memory by ID.

**Code graph** (Go repos)
- `graph_symbol` — a symbol's source plus callers/callees (and interface↔concrete
  `implements` edges) as signature stubs.
- `graph_package` — a package's exported surface, signatures only.

Graph responses render as **TOON** (Token-Oriented Object Notation) — one shared
header per neighbor array instead of repeating JSON keys on every row — to keep
"what calls X" answers cheap on hub symbols.

Operator commands (`list`, `schema`, `doctor`, `migrate`, `prune`, `scrub`) are
**not** exposed over MCP — they're CLI-only by design.

```shell
droids-mem ensure-server   # ping /healthz, spawn detached serve if down
droids-mem serve           # foreground MCP bridge (Streamable HTTP)
droids-mem serve --stdio   # MCP over stdin/stdout (for codex, opencode, cursor)
```

Auth: `Authorization: Bearer <token>` on every `/mcp` request.
`/identity?nonce=<n>` answers `HMAC-SHA256(token, nonce)` — anti port-squatting.

---

## CLI reference

| Command | What it does |
|---|---|
| `save` | Save a structured memory (scrubs + dedupes) |
| `search` | Full-text search across memories |
| `context` | Load a start-of-run context bundle for a task type |
| `get` | Fetch one memory by ID |
| `list` | List recent memories |
| `tui` | Interactive terminal browser |
| `prune` | Delete memories or find duplicate clusters |
| `graph` | Query a Go repo's code graph (index, symbol, package) |
| `recent-sessions` | List auto-saved session summaries |
| `session` | Session-memory plumbing (stage, check, flush, recover, hook) |
| `install` | Wire into a host: Claude Code hooks, or `--host codex\|opencode` |
| `uninstall` | Reverse `install`: unwire hooks, deregister bridge, strip CLAUDE.md block |
| `doctor` | FTS integrity/rebuild, optimize, VACUUM, scrub stats |
| `schema` | Show parameter schema for a command |
| `scrub` | Run the scrub engine ad-hoc (`--check`, `--test`) |
| `migrate` | Establish the scrub baseline on an existing database |
| `serve` / `ensure-server` | Run or start the MCP bridge |

Every command supports `--help`.

---

## Shared context

Memory is local and private by default — every row has a `scope`
(`personal` | `shared`) that defaults to `personal`, so nothing leaves your
store implicitly. Opt in per memory from the TUI: flip rows to `shared`, then
**publish** them into a dedicated **git-tracked pool** (`shared.jsonl`, one file
per `task_type`) that teammates **pull** into their own store.

- Sharing is explicit and per-row — never a whole-corpus dump.
- Every shared copy is **scrubbed** at the trust boundary (local paths, session
  ids, and timestamps are stripped; content, tags, and kind cross over).
- Import **dedupes across sources** by fingerprint + Jaccard, so the same lesson
  from two teammates lands once.

Shared copies enter the git-tracked pool and can't be fully retracted — anyone
who pulled keeps their copy. The TUI confirm dialog spells this out before push.

---

## Secret scrub

Every `save` runs text through a single-pass scrub pipeline before it touches
the database. Detectors run in three classes:

- **Provider tokens**: PEM keys, JWTs, AWS/GitHub/GitLab/Google/npm/Stripe/
  Slack/Anthropic/OpenAI keys.
- **Usage patterns**: bearer headers, `key = value` assignments, URL credentials
  — gated on Shannon entropy so `password = changeme` survives but real secrets
  don't.
- **PII**: phone numbers, private IPv4 addresses, emails.

Longer redaction span wins on overlap. Tags that match any pattern **reject**
the save (no silent auto-strip).

Field caps: `title=200`, `what=8192`, `learned=4096`, `tags=500` — exceeding
any returns `field_too_large`.

```shell
droids-mem scrub --check /path/to/some.log   # ad-hoc, no DB write
droids-mem scrub --test                       # run the fixture corpus
droids-mem doctor --scrub-stats               # aggregate counts across the DB
```

---

## Configuration

All optional. Defaults match a single-user laptop install.

| Var | Default | Notes |
|---|---|---|
| `DROIDS_MEM_DB` | `~/.droids-mem/mem.db` | Database file path |
| `DROIDS_MEM_HOME` | `~/.droids-mem/` | Token, pid, log files |
| `DROIDS_MEM_MCP_TOKEN` | Auto-generated | Bearer token for `/mcp` |
| `DROIDS_MEM_MCP_ADDR` | `127.0.0.1:7777` | Bind address (non-loopback logs a warning) |
| `DROIDS_MEM_MCP_ENDPOINT` | `/mcp` | `/healthz` + `/identity` always unauthenticated |

State directory: `mem.db` (0600), `token` (0600), `mcp.pid`, `mcp.log`.

---

## Troubleshooting

**Boot gate on start.** The database hasn't been through the scrub pipeline. The
first non-bypassed command auto-runs `migrate --rescrub` (one-time write). If it
fails, run `droids-mem migrate --rescrub` by hand.

**`db_init_failed`.** Check `DROIDS_MEM_DB` and that `~/.droids-mem/` is
writable.

**`tag_contains_secret`.** A tag matched a scrub pattern. Tags aren't
auto-stripped — fix the tag and retry.

**`scrub_emptied_learned`.** The `learned` field was fully redacted. Rewrite
the lesson without the PII.

**MCP bridge won't bind.** Default `127.0.0.1:7777` conflicts if another
listener holds the port. Verify using `/identity?nonce=...`.

**Stale FTS results.** `droids-mem doctor` rebuilds `memories_fts` from
`memories` if they diverge.

---

## Contributing

Found a bug? Have an idea? Open an issue or pull request on
[GitHub](https://github.com/SamuelMolero26/droids-mem).

This project is early and open to contributors who share its philosophy: local-
first, no external dependencies, simple beats flexible. PRs that add new
external services will be rejected.

---

## License

[MIT](LICENSE). See [CHANGELOG.md](CHANGELOG.md) for release history.
