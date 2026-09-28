# Feature: playwright-ui-e2e

## Objective
Add full-daemon Playwright e2e for the graph-ui redesign: package.json at repo root, Playwright boots the real loopback daemon with a signed UI key and tests map/symbol/flow against live /api/graph.

## Problem
`feat/graph-ui-redesign` adds 7 new vanilla-JS UI files (`internal/mcpserver/ui.go` + `ui/{index.html,app.css,core.js,map.js,symbol.js,views.js}`, ~1050 lines, `//go:embed ui`, CSP `default-src 'none'`, loopback-only + `UIKey` bearer). Existing CI (`ci.yml`) is Go-only (build/test/lint/govulncheck/gosec/dependency-review/CodeQL-go). A JS-only edit (syntax error, broken fetch contract, DOM regression) stays green.

## Why
User explicitly chose **Full daemon e2e** over ui-local smoke and lint-only: package.json at repo root, Playwright against the real `graph ui` daemon with signed key.

## Scope
- In: `package.json` (+ lock) at root, Playwright config + harness + spec, CI e2e job, `.gitignore` entries, embed-safety proof.
- Out: UI redesign itself, Go behavior changes, release.yml changes, TS migration, visual snapshots.

## Constraints
- Vanilla JS stays vanilla: no bundler, no framework, no TS.
- All third-party GH Actions SHA-pinned with version comment (existing hardening rule).
- Root `node_modules/` must never enter `//go:embed ui` (embed root is `internal/mcpserver/ui/` only — verify).
- Isolate daemon per run: temp `DROIDS_MEM_HOME`/`DROIDS_MEM_DB`, loopback only, no `openBrowser` dependence (failure is non-fatal by design).
- CI runtime target ~<5 min for the e2e job (browser install cached).

## Authorized scope
User: "I want to add a playwright check for the UI testing framework and package management" + selected "Full daemon e2e". Branch: `feat/graph-ui-redesign` (worktree 6). No SDD, no delivery (push/PR) without explicit ask.

## TDD
- Resolved: OFF for JS (source: none — no pre-existing JS runner; Go strict-TDD from sdd-init memory applies to `go test ./...` only, not to new JS harness).
- Runner (to be installed): `npx playwright test`.
- Ordinary functional checks apply: `npm ci`, `npx playwright test`, `go build ./...`, `go test ./internal/mcpserver/`.

## Delivery
- Strategy: `ask-on-risk` (default).
- Forecast: ~300 authored lines (pkg ~30, config+harness ~90, spec ~150, CI ~40, gitignore/docs ~20; browsers/lock excluded) — under 400, single PR expected.
- Running count: 0 (reconcile from work-unit commits).

## Tasks
- [x] T1 scaffold-root-package — package.json at root (`@playwright/test`), package-lock, .gitignore (node_modules, test-results, playwright-report). Route: inline (1 non-trivial file + lock, mapping already held). Acceptance: `npm ci` clean, `git check-ignore node_modules` proves embed-safe (embed dir untouched). DONE 2026-09-28: npm install + npm ci clean (3 pkgs, @playwright/test 1.63.0), `go build ./cmd/droids-mem` exit 0, `git check-ignore` → .gitignore:30.
- [x] T2 playwright-harness — playwright.config.js + e2e helper that builds binary, starts loopback daemon with isolated HOME, runs `graph ui` to mint `#k=` URL, exposes baseURL+key. Route: inline (explore delegation refused this runtime; standing evidence recorded). Acceptance: helper prints valid `/ui/#k=v1.` URL against fixture repo. DONE 2026-09-28: `graph ui --repo e2e/fixture` → URL_SHAPE ok, /ui/ 200, /api/graph/overview 200 with 2 pkgs.
- [x] T3 e2e-spec — spec covers Map (nodes render), Symbol (three-pane), Flow (callers/focus/callees) + search/trail against live /api/graph; every string via text nodes (CSP). Route: inline (same writer as T2). Acceptance: `npx playwright test` green locally (chromium). DONE 2026-09-28: 5/5 pass (2.0s). Two assertion fixes: UI shows DM.short names (Add, calc/calc.go:5), not full qnames.
- [x] T4 ci-job — e2e job in `.github/workflows/ci.yml` (or `ui.yml` if cleaner): pinned setup-node + setup-go, npm ci cache, `npx playwright install --with-deps chromium`, run e2e, upload report on failure. Route: inline (1 file). Acceptance: workflow parses (`actionlint` if available else `python - yaml`), pins have SHA+comment. DONE 2026-09-28: yaml parses, 6 jobs, all uses: SHA-pinned (setup-node v5.0.0 a0853c2, upload-artifact v5.0.0 330a01c).
- [x] T5 verify-close — full verify (`npm ci`, `npx playwright test`, `go build ./...`), embed-safety re-proof, record commit identities, update this doc. Route: inline. DONE 2026-09-28: npm ci clean, 5/5 pass, go build ./... exit 0, ui/ holds only 6 assets.

## Route declaration (trigger evidence)
- Mapping trigger fired (7 UI files > 4): attempted one `explore` delegation, failed — `OpenCode's free tier can only be used from within OpenCode`. Fell back inline with `git show` reads; evidence above.
- Writer trigger will fire for T2+T3 (2+ non-trivial files): will attempt one bounded writer first; on same runtime refusal, continue inline and record fallback here.

## Progress
- 2026-09-28: doc created, no source writes yet. Engram mirror PENDING (server unavailable, droids-mem mirror saved mem_01M3M8A9Y75Z3N7ZNC6FVZBBXS). Next: T1.

## Verification evidence
- None yet.

## Next step
- T1 scaffold-root-package.
