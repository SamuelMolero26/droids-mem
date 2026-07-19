package tui

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestEnsureRepoInit_CreatesRepo covers the "name a repo that doesn't exist yet
// and move on" path: ensureRepoInit must create the dir and git-init it so a
// subsequent strict ensureRepo (the pull-side check) accepts it.
func TestEnsureRepoInit_CreatesRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "pool") // does not exist yet

	if err := ensureRepoInit(ctx, dir); err != nil {
		t.Fatalf("ensureRepoInit on fresh path: %v", err)
	}
	if err := ensureRepo(ctx, dir); err != nil {
		t.Fatalf("created path is not a git work tree: %v", err)
	}
	// Idempotent: a second call on the now-existing repo is a no-op.
	if err := ensureRepoInit(ctx, dir); err != nil {
		t.Fatalf("ensureRepoInit on existing repo: %v", err)
	}
	// A fresh local repo has no remote — pushShared must skip the push.
	if hasRemote(ctx, dir) {
		t.Fatal("fresh repo unexpectedly reports a remote")
	}
}
