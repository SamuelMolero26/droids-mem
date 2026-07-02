<!-- markdownlint-disable MD041 -->
```text
                ████▒  █████   ▓██▓  █████  ████▒   ▓███▒        █▒  ▒█ ██████ █▒  ▒█
                █  ▒█░ █   ▓█ ▒█  █▒   █    █  ▒█░ █▓  ░█        ██  ██ █      ██  ██
                █   ▒█ █    █ █░  ░█   █    █   ▒█ █             ██░░██ █      ██░░██
                █    █ █   ▒█ █    █   █    █    █ █▓░           █▒▓▓▒█ █      █▒▓▓▒█
                █    █ █████  █    █   █    █    █  ▓██▓         █ ██ █ ██████ █ ██ █
                █    █ █  ░█▒ █    █   █    █    █     ▓█  ███   █ █▓ █ █      █ █▓ █
                █   ▒█ █   ░█ █░  ░█   █    █   ▒█      █        █    █ █      █    █
                █  ▒█░ █    █ ▒█  █▒   █    █  ▒█░ █░  ▓█        █    █ █      █    █
                ████▒  █    ▒  ▓██▓  █████  ████▒  ▒████░        █    █ ██████ █    █
                                                    
```

# droids-mem

Persistent memory for AI agents. SQLite + FTS5, single binary, zero external
dependencies. CLI for humans; an MCP bridge for agents.

- **What it stores:** structured lessons — session summaries, task patterns,
  error resolutions, user rules — written by an agent and replayed at the start
  of the next run.
- **What it does on save:** validates → scrubs PII → fingerprint-dedupes →
  near-dup-dedupes (BM25 + Jaccard) → persists. All inside one
  `BEGIN IMMEDIATE` transaction.
- **What it does on read:** a two-tier context bundle — `always` (last session
  + user rules, full body) + `browse` (top error resolutions + task patterns,
  snippets) — keyed on `task_type`.

Single global DB at `~/.droids-mem/mem.db`. No service, no daemon required;
the MCP bridge spawns on demand via `ensure-server`.

> Status: **v1.0.0** (2026-06-09). Workspaces (ADR-0005) and git-JSONL sync
> (ADR-0006) ship in v1.1 / v1.2. See [CHANGELOG.md](CHANGELOG.md).

---

## Install

### `go install`

```
go install github.com/SamuelMolero26/droids-mem/cmd/droids-mem@latest
```

Requires Go 1.25+. The binary is pure-Go (`modernc.org/sqlite`), so it builds
without CGO.

### Prebuilt binaries

Each release attaches binaries + `.sha256` checksums for
`linux/{amd64,arm64}` and `darwin/{amd64,arm64}` on the
[Releases page](https://github.com/SamuelMolero26/droids-mem/releases).

```
curl -L -o droids-mem \
  https://github.com/SamuelMolero26/droids-mem/releases/download/v1.0.0/droids-mem-v1.0.0-darwin-arm64
chmod +x droids-mem
./droids-mem --version
```

### Build from source

```
git clone https://github.com/SamuelMolero26/droids-mem
cd droids-mem
go build ./cmd/droids-mem
./droids-mem --version
```

---

## Quick start

```
# Save a lesson the agent learned
droids-mem save \
  --task-type "go-backend" \
  --kind error_resolution \
  --title "modernc sqlite returns Error code 5 on FTS5 trigger rebuild" \
  --what "memories_fts rebuild in same txn as ALTER TABLE deadlocked under contention" \
  --learned "DROP + INSERT-SELECT must run after the parent ALTER commits"

# Search by full-text
droids-mem search --query "fts5 rebuild" --limit 5

# Load context for the start of a run
droids-mem context --task-type "go-backend"

# Health check + scrub stats
droids-mem doctor --scrub-stats
```

All output is JSON to stdout; errors are JSON to stderr. Exit codes:
`0` ok, `1` runtime, `2` usage, `3` not found, `5` conflict/duplicate,
`10` dry-run pass.

### First run on an existing pre-v1.0 database

v1.0 refuses to boot against a database that has not been baselined through
the scrub pipeline. Pick one:

```
droids-mem migrate --rescrub      # rewrite every row through the scrub pipeline
droids-mem migrate --no-rescrub   # acknowledge plaintext, set the sentinel only
```

Both forms are atomic per DB. After either completes, the v1.0 binary boots.
Fresh installs do not need this step — the baseline sentinel is written by the
first `db.Init` on a new file.

---

## Use with Claude Code

Two complementary layers:

1. **MCP tools** — give the agent `mem_save` / `mem_search` / `mem_context` /
   `mem_get` to read and write memory on demand.
2. **Session memory** — guarantee a memory is recorded at the **end of every
   session** and surface relevant prior memories when you start related work,
   via Claude Code hooks handled natively by the binary (no shell scripts, no
   `jq`). See [ADR-0016](docs/adr/0016-native-claude-code-session-auto-summary.md).

You can enable either layer on its own; together they give the full experience.

```
# 0. Install the binary (if you haven't — see Install above)
go install github.com/samuelmolero/droids-mem/cmd/droids-mem@latest
```

### One-shot bootstrap

```
droids-mem install --all
```

Does everything below in one idempotent command: merges the hooks into
`~/.claude/settings.json`, starts the MCP bridge (`ensure-server`), registers
it with the Claude Code CLI at user scope (`claude mcp add`), and appends the
compose-guidance block to `~/.claude/CLAUDE.md`. Each step reports its own
status; a missing `claude` CLI degrades to a manual instruction instead of
failing the rest. The steps below remain for manual / non-Claude setups.

### 1. Add the MCP tools

```
# Start the local MCP bridge (idempotent — spawns a detached server if down)
droids-mem ensure-server

# Register it with Claude Code (HTTP transport + bearer token)
claude mcp add --transport http droids-mem http://127.0.0.1:7777/mcp \
  --header "Authorization: Bearer $(tr -d '\n' < ~/.droids-mem/token)"
```

The agent now has the four `mem_*` tools. (Bind address and token are
configurable — see [Configuration](#configuration).)

### 2. Add guaranteed session memory

```
# Wire the hooks into Claude Code's settings.json (idempotent, non-destructive)
droids-mem install

# Tell the model when to record a summary
cat cmd/droids-mem/claude_snippet.md >> ~/.claude/CLAUDE.md
```

`install` merges hook entries into `~/.claude/settings.json`, pointing every
event at `droids-mem session hook`. Options: `--project` targets
`./.claude/settings.json`; `--print` previews the block without writing.

`droids-mem session hook` reads each hook's JSON on stdin and dispatches:

| Claude Code event | Behaviour |
|-------------------|-----------|
| `PostToolUse` (Edit/Write/Bash/…) | count meaningful changes (intake gate) |
| `Stop` | once enough work is unstaged, ask the model to record progress |
| `SessionEnd` | save the staged summary if the gate passes |
| `SessionStart` | start the MCP bridge if down; recover summaries from crashed runs |
| `UserPromptSubmit` | inject relevant prior memories for the prompt |

Every hook **fails open** — a memory hiccup never breaks your session.

### Verify

```
claude mcp list                       # droids-mem should be listed + reachable
droids-mem recent-sessions            # after a session with edits: your auto-summaries
```

Full hook reference: [`hooks/README.md`](hooks/README.md).

---

## Secret scrub behaviour

On every `save`, `title` / `what` / `learned` are scrubbed in a single pass.
Tags, `task_type`, and `session_id` are checked against the same detectors and
the save is **rejected** on any match (`tag_contains_secret`,
`task_type_contains_secret`, `session_id_contains_secret` — all
`retryable:true`) because those fields are stored unscrubbed.

Detectors are declared in `internal/scrub/spec.yaml` (pattern version 3) in
three classes. Overlap resolution: longer redaction span wins, tie → earlier
declaration wins, so a provider token inside an assignment keeps its specific
redaction token.

| # | Detector | Class | Token |
|---|----------|-------|-------|
| 1 | PEM private key block | provider | `[PEM_KEY]` |
| 2 | JWT (`xxx.yyy.zzz`) | provider | `[JWT]` |
| 3 | AWS access key (`AKIA…`, `ASIA…`) | provider | `[AWS_KEY]` |
| 4 | GitHub token (`ghp_`, `gho_`, `ghu_`, `ghs_`, `ghr_`) | provider | `[GITHUB_TOKEN]` |
| 5 | GitHub fine-grained PAT (`github_pat_`) | provider | `[GITHUB_TOKEN]` |
| 6 | GitLab PAT (`glpat-`) | provider | `[GITLAB_TOKEN]` |
| 7 | Google API key (`AIza…`) | provider | `[GOOGLE_KEY]` |
| 8 | npm token (`npm_`) | provider | `[NPM_TOKEN]` |
| 9 | Stripe key (`sk_live_`, `pk_live_`, …) | provider | `[STRIPE_KEY]` |
| 10 | Slack token (`xoxa-` … `xoxs-`) | provider | `[SLACK_TOKEN]` |
| 11 | Anthropic API key (`sk-ant-`) | provider | `[ANTHROPIC_KEY]` |
| 12 | OpenAI API key (`sk-`) | provider | `[OPENAI_KEY]` |
| 13 | Bearer header value (`Bearer <value>`) | usage | `[SECRET]` |
| 14 | Assignment value (`key/token/password [:=] <value>`) | usage | `[SECRET]` |
| 15 | URL credential (`scheme://user:<password>@`) | usage | `[SECRET]` |
| 16 | Phone (E.164) | pii | `[PHONE]` |
| 17 | Private IPv4 (RFC 1918 + loopback) | pii | `[PRIVATE_IP]` |
| 18 | Email | pii | `[EMAIL]` |

Usage-class detectors redact only the secret value (context words stay
readable) and — for bearer/assignment — must pass a deterministic Shannon
entropy gate (≥ 3.5 bits/char), so placeholders like `password = changeme`
are left alone while generated secrets are redacted. See
`docs/adr/0008-layered-scrub-detectors.md`.

Empty-after-scrub on `learned` rejects the save with `scrub_emptied_learned`.
Save responses include a `scrub` block whenever `redaction_count > 0`.

Ad-hoc check without touching the DB:

```
droids-mem scrub --check /path/to/some.log
droids-mem scrub --test                  # run the fixture corpus
droids-mem doctor --scrub-stats          # aggregate counts across DB rows
```

Field caps: `title=200`, `what=8192`, `learned=4096`, `tags=500`. Exceeding any
returns `field_too_large`.

---

## Configuration

All optional. Defaults match a single-user laptop install.

| Var | Default | Notes |
|-----|---------|-------|
| `DROIDS_MEM_DB` | `~/.droids-mem/mem.db` | DB file path |
| `DROIDS_MEM_HOME` | `~/.droids-mem/` | token, pid, log files |
| `DROIDS_MEM_MCP_TOKEN` | auto-generated | Bearer token for `/mcp` |
| `DROIDS_MEM_MCP_ADDR` | `127.0.0.1:7777` | Bind address (non-loopback logs a warning) |
| `DROIDS_MEM_MCP_ENDPOINT` | `/mcp` | `/healthz` + `/identity` always unauthenticated |

State dir layout: `mem.db` (0600), `token` (0600), `mcp.pid`, `mcp.log`.

---

## MCP bridge

The agent-facing surface. Four tools:

- `mem_save` — persist a lesson. Accepts an optional `scope` (`personal` |
  `shared`, default `shared`).
- `mem_search` — full-text search with BM25 ranking.
- `mem_context` — two-tier context bundle for a `task_type`. Mints a stateless
  `session_id` the agent threads through subsequent calls.
- `mem_get` — fetch a single memory by ID.

Operator commands (`list`, `schema`, `doctor`, `migrate`, `scrub`) are
**intentionally** not exposed over MCP.

```
droids-mem ensure-server   # ping /healthz, spawn detached serve if down
droids-mem serve           # foreground MCP bridge
```

Auth is `Authorization: Bearer <token>`. `/identity?nonce=<n>` answers
`HMAC-SHA256(token, nonce)` so callers can verify the listener actually holds
the token (anti port-squatting).

---

## Subcommand index

| Command | Summary |
|---------|---------|
| `save` | Save a structured memory |
| `search` | Search memories using full-text search |
| `context` | Load start-of-run context bundle for a task type |
| `get` | Get a single memory by ID |
| `list` | List recent memories |
| `recent-sessions` | List recent auto-saved Claude Code session summaries |
| `session` | Claude Code session-memory plumbing (stage, check, flush, recover, hook) |
| `install` | Wire droids-mem session memory into Claude Code (settings.json hooks) |
| `doctor` | Check FTS integrity, rebuild if divergent, optimize, VACUUM, `--scrub-stats` |
| `schema` | Show parameter schema for a command (or all commands) |
| `scrub` | Run the v1.0 scrub engine ad-hoc (`--check`, `--test`) |
| `migrate` | Establish the v1.0 scrub baseline on an existing database |
| `serve` | Run the MCP bridge server |
| `ensure-server` | Start the MCP bridge if it is not already running |

Every command supports `--help`. `droids-mem schema` emits machine-readable
parameter schemas for scripting.

---

## Architecture & decisions

Single binary, layered. Don't bypass layers:

1. `cmd/droids-mem/` — cobra subcommands. One `cmd_*.go` per command. No
   business logic.
2. `internal/mcpserver/` — MCP bridge (HTTP + bearer auth + 4 tools).
3. `internal/store/` — all business logic shared by CLI and MCP (save,
   search, context, doctor, scrub).
4. `internal/db/` — connection + pragmas + DDL + `PRAGMA user_version`
   migration ladder.
5. `internal/state/` — bearer-token resolver, owns `~/.droids-mem/` file ops.

Architecture decision records (ADRs) live under `docs/adr/`:

- [0001 — Fingerprint excludes `what`](docs/adr/0001-fingerprint-excludes-what.md)
- [0002 — Context bundle tier model](docs/adr/0002-context-bundle-tier-model.md)
- [0003 — MCP transport, bearer auth, session ownership](docs/adr/0003-mcp-bridge-for-agentspan.md)
- [0004 — Parent-as-memory-broker pattern](docs/adr/0004-agent-broker-pattern.md)
- [0005 — Three-layer workspace model](docs/adr/0005-three-layer-workspace-model.md) (Deferred to v1.1)
- [0006 — Git-native JSONL sync](docs/adr/0006-git-jsonl-sync-for-project-workspaces.md) (Deferred to v1.2)
- [0007 — PII scrub pipeline](docs/adr/0007-pii-scrub-pipeline.md) (Accepted)

Reference docs:

- [CONTEXT.md](CONTEXT.md) — domain language + term aliases.

---

## Troubleshooting

**`boot_gate` error on start.** The database has not been baselined through
the scrub pipeline. Run `droids-mem migrate --rescrub` (or `--no-rescrub` if
the data is already trusted).

**`db_init_failed`.** Check `DROIDS_MEM_DB` and that `~/.droids-mem/` is
writable. State dir is created on first use.

**`tag_contains_secret`.** A tag matched a scrub pattern. Tags are not
auto-stripped — fix the tag and retry (`retryable:true`).

**`scrub_emptied_learned`.** The `learned` field was 100% redaction after
scrub. Rewrite the lesson without the PII.

**MCP bridge will not bind.** Check `DROIDS_MEM_MCP_ADDR`. The default
`127.0.0.1:7777` will conflict if another listener has the port; verify it
actually holds your token via `curl /identity?nonce=...`.

**FTS results look stale or wrong.** `droids-mem doctor` runs an integrity
check and rebuilds `memories_fts` from `memories` if they diverge.

---

## License

[MIT](LICENSE).
