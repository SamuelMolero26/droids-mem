## droids-mem code graph

For code questions in a Go, Python, TypeScript, or JavaScript repo, use the
droids-mem graph tools before grep or reading files. Pass this project's root
as `repo`.

- `graph_package` — orient in an area (exported surface, signatures only).
- `graph_symbol` — one symbol's source plus callers/callees. Use
  `direction=up depth=3` before editing to see the blast radius.
