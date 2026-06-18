# Memory inspector TUI

A BubbleTea terminal UI (`droids-mem tui`) for browsing the local memory corpus: a list view that filters (by kind/task_type) and live-searches, drills into a full-screen detail view, and deletes a single Memory through a confirmation dialog. Lives in `internal/tui/` (models) behind a thin `cmd_tui.go` wire-up, talking to the store in-process. Interactive (not read-only) and single-corpus; multi-select bulk delete and a live save-feed are deferred.

## Store dependency is a narrow port, not the concrete Store

`internal/tui` depends on a four-method `memStore` interface (`List`, `Search`, `GetRow`, `Prune`) that `*store.Store` satisfies; `cmd_tui.go` injects the real store. This is a genuine seam (two adapters: production `*Store` + a test fake), unlike the Expand-signal recorder (ADR-0013) which stayed concrete because it had only one. The payoff is testability: the model's logic — does a delete refresh the list, does sub-3-rune input skip search, does a stale search result get dropped — is exercised against an in-memory fake with canned responses, no SQLite, no fixtures.

## Detail loads are uniform and non-counting

`List` returns full `Memory` rows; `Search` returns `SearchResult` without `what`. Rather than branch on the source, the list holds a thin projection `{id, kind, title, task_type, created_at}` (the common denominator of both), and the detail view **always** calls `GetRow(id)` on enter. One load path, always-fresh data (not a stale list snapshot), and `GetRow` is the non-counting fetch (ADR-0013) so operator browsing never pollutes the Expand signal. The confirm dialog reads the title straight from the projection — no fetch.

## Async + the debounced-search race

All store I/O runs in `tea.Cmd`s (the `Update` loop never blocks). Because in-flight Cmds can't be cancelled, fast typing can deliver an older search's results after a newer one's. A single monotonic **generation counter** handles both debounce and staleness: each keystroke bumps `gen` and schedules a 200 ms `tea.Tick` carrying it; the tick fires a `Search` (stamped with `gen`) only if its `gen` is still current and the query is ≥3 runes; results whose `gen` is stale are discarded in `Update`. The ≥3-rune floor checked at fire-time means deleting back below 3 chars cancels a pending search.

## Mutation loop

Confirm → `Prune{ID, Apply:true}` Cmd (ADR-0014) → `deletedMsg` → re-issue the model's single current-query descriptor (the active filter or search, never a blanket list) so the view stays consistent minus the deleted row → cursor clamps to the new bounds; empty result shows an empty-state line. Every deletion routes through `store.Prune`, keeping the FTS-sync and transaction discipline in one place.
