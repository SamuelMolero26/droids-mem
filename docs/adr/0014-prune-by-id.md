# Prune gains id-targeted deletion

`PruneRequest` gains an optional `ID` field. When set, deletion targets that single Memory by exact id and the other filters (`kind`, `task_type`, `older_than_days`) are ignored; the id itself satisfies the `prune_unfiltered` guard. Surfaced as `prune --id <id>` on the CLI and used by the Memory inspector (ADR-0015) for single-item delete.

All deletion stays inside one **Prune** method rather than a separate `Delete`, so the `BEGIN IMMEDIATE` discipline and FTS sync (the AD trigger — never touch `memories_fts` directly) are enforced in exactly one place. A single-id delete is still "the explicit, human-initiated deletion workflow" CONTEXT.md defines Prune to be — just with the tightest possible filter. This also makes real the `--suggest-dupes` help text that already told operators to "prune by id," a capability the API never had until now.

When `ID` is set but no row matches (e.g. concurrently deleted), Prune returns `count=0` / `pruned` rather than a not-found error — consistent with filter-prune matching nothing, and idempotent for the inspector's refresh-after-delete loop.
