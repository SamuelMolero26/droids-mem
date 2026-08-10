package share

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeGitHoldingPipes installs a `git` first on PATH that spawns a background
// child inheriting stdout/stderr and then sleeps. Killing the git process does
// not close those pipes, because the grandchild still holds them.
//
// This is what `git pull` over ssh does for real: git is killed when the
// context expires, ssh is not, and cmd.Run blocks on the inherited pipes until
// ssh gives up on its own — measured at 73s against an unroutable host, for a
// call whose context deadline was 15s.
func fakeGitHoldingPipes(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nsleep 60 &\nsleep 60\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o700); err != nil { // #nosec G306 -- test stub must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

// A cancelled context must actually return control. Without WaitDelay, Run
// blocks on the grandchild's pipes long past the deadline, so every timeout
// this package documents is advisory rather than enforced.
func TestRunGit_ReturnsPromptlyWhenContextExpires(t *testing.T) {
	fakeGitHoldingPipes(t)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := runGit(ctx, t.TempDir(), "pull")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error from the cancelled git invocation")
	}
	// Deadline plus WaitDelay plus slack. A regression here means minutes.
	if elapsed > 5*time.Second {
		t.Errorf("runGit took %v after a 300ms deadline — the pipes are not bounded", elapsed)
	}
}

// The wrapped cause must survive so callers can tell a timeout from a genuine
// git failure; the message alone cannot distinguish them.
func TestRunGit_TimeoutCauseIsRecoverable(t *testing.T) {
	fakeGitHoldingPipes(t)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := runGit(ctx, t.TempDir(), "pull")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "git pull") {
		t.Errorf("error lost its command context: %v", err)
	}
}
