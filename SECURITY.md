# Security Policy

## Reporting a vulnerability

Report vulnerabilities privately via GitHub's security tab (the preferred path):
https://github.com/SamuelMolero26/droids-mem/security/advisories/new

For Go stdlib or module CVEs, run `govulncheck ./...` and include its output in
the report.

## Response expectations

- Acknowledgment within 7 days of a valid report.
- Patch timeline: critical/high severity within 14 days; moderate/low on the
  next release cadence (PR develop to main, then a tag on main).
- Coordinated disclosure only: do not post details publicly before the fix is
  released.

## Scope

- droids-mem runtime (CLI, `serve`, `ensure-server`)
- MCP bridge authentication: token file permissions (0600), loopback-only
  binding by default, bearer token verification
- Scrub engine (detector spec, entropy gating)
- Shared-pool git transport (see `files/shared-context-sync-PRD.md`)

## Policy

- No bug bounties are offered.
- Acknowledged vulnerabilities are fixed and disclosed publicly after the fix
  ships, with a GitHub Security Advisory.
