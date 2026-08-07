## Checklist

- [ ] **Changelog**: entry added under [Unreleased] in CHANGELOG.md if user-facing behavior changed
- [ ] **Tests**: added/updated for changed behavior; `go test -race -shuffle=on ./...` passes
- [ ] **Lint**: `golangci-lint run --timeout 5m` is clean
- [ ] **Dependencies**: go.mod/go.sum changes verified with `go mod verify`
- [ ] **Commit**: conventional commits, no AI attribution
