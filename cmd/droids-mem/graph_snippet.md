## droids-mem code graph

For structural code work in supported Go, Python, TypeScript, or JavaScript
repositories, use droids-mem before grep, glob, reading files, or shell search.
Pass this project's absolute root as `repo`.

- `graph_package` — orient in an area (exported surface, signatures only).
- `graph_symbol` — one symbol's source plus callers/callees. Use
  `direction=up depth=3` before editing to see the blast radius.
- If droids-mem tools are hidden or pending, use `ToolSearch` to select
  `graph_package` or `graph_symbol`.
- If a graph response says it is rebuilding, call `graph_build_wait`, then
  retry the graph query.
- Verify `stale`, `changed`, `carried`, or `stale_units` results against current
  source.
- Fall back to source search only when graph tooling is unavailable or returns an error.
