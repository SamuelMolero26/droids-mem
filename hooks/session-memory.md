# Session memory (droids-mem) — compose guidance

A block in your `CLAUDE.md` (global or project) is the model-judgment half of
the intake gate (ADR-0016): the hooks guarantee *when* to consider saving; the
block tells the model *whether* and *how* to compose.

The canonical block lives at
[`cmd/droids-mem/claude_snippet.md`](../cmd/droids-mem/claude_snippet.md)
(embedded in the binary). Install it either way:

```
# One-shot bootstrap (hooks + server + MCP registration + CLAUDE.md block)
droids-mem install --all

# Or append just the block manually (from a repo checkout)
cat cmd/droids-mem/claude_snippet.md >> ~/.claude/CLAUDE.md
```

Both are idempotent — a CLAUDE.md already containing the block is left
untouched by `install --all`.
