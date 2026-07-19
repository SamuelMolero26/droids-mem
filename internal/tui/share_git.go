package tui

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/samuelmolero26/droids-mem/internal/store"
)

// sharedFile is the fixed pool filename inside the target repo. One file per
// repo — every share appends to the same corpus, which is exactly the "reuse
// the same repo" the git-tracked pool wants (ADR-0028).
const sharedFile = "shared.jsonl"

// runGit runs `git -C dir args...`, returning combined output. Git writes most
// of its useful diagnostics (auth failure, dirty tree, no upstream) to stderr,
// so we fold both streams into the error the TUI toasts.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	// #nosec G204 -- command is the constant "git"; dir is the user's own repo
	// path and args are all hardcoded call-site literals, none attacker-supplied.
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	// GIT_TERMINAL_PROMPT=0: never prompt for credentials on the controlling
	// terminal — BubbleTea owns it, so a prompt would garble/hang the TUI. Auth
	// failure comes back as a clean error the toast shows instead.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(out.String()))
	}
	return out.String(), nil
}

// pushShared exports the whole scope='shared' pool into repoDir/shared.jsonl,
// commits it, and pushes if the repo has a remote (ADR-0028, full-automation
// share). The repo is created + `git init`d when the path isn't one yet, so the
// user can name a fresh pool and move on; a remote-less repo just commits
// locally. Nothing to commit is a no-op, not an error — re-sharing an
// already-pushed memory shouldn't fail the action.
// ponytail: no merge-conflict resolution — a rejected push surfaces git's own
// message in the toast and the user reconciles in their terminal.
func pushShared(ctx context.Context, repoDir string, s memStore, n int) error {
	if err := ensureRepoInit(ctx, repoDir); err != nil {
		return err
	}
	path := filepath.Join(repoDir, sharedFile)
	// #nosec G304 -- repoDir is a user-chosen path they own; writing their pool.
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", sharedFile, err)
	}
	if err := s.ExportShared(ctx, f); err != nil {
		_ = f.Close()
		return fmt.Errorf("export shared: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", sharedFile, err)
	}

	if _, err := runGit(ctx, repoDir, "add", sharedFile); err != nil {
		return err
	}
	msg := fmt.Sprintf("share: %d %s", n, plural(n, "memory", "memories"))
	if out, err := runGit(ctx, repoDir, "commit", "-m", msg); err != nil {
		if strings.Contains(out, "nothing to commit") {
			return nil // pool already up to date — not a failure
		}
		return err
	}
	if !hasRemote(ctx, repoDir) {
		return nil // freshly-created local pool: committed, nothing to push to yet
	}
	if _, err := runGit(ctx, repoDir, "push"); err != nil {
		return err
	}
	return nil
}

// pullShared pulls repoDir then imports repoDir/shared.jsonl into the local
// store (the consume half of the pool). Import is resilient per-row (ADR-0028),
// so a poisoned line is counted in Failed, not fatal.
func pullShared(ctx context.Context, repoDir string, s memStore) (store.ImportResult, error) {
	if err := ensureRepo(ctx, repoDir); err != nil {
		return store.ImportResult{}, err
	}
	if _, err := runGit(ctx, repoDir, "pull"); err != nil {
		return store.ImportResult{}, err
	}
	path := filepath.Join(repoDir, sharedFile)
	// #nosec G304 -- repoDir is a user-chosen path they own.
	f, err := os.Open(path)
	if err != nil {
		return store.ImportResult{}, fmt.Errorf("open %s: %w", sharedFile, err)
	}
	defer f.Close()
	return s.ImportShared(ctx, f)
}

// ensureRepo rejects a path that isn't a git work tree, so the toast says
// "not a git repo" instead of a cryptic add/commit failure. Used by pull —
// you can only consume a pool that already exists.
func ensureRepo(ctx context.Context, repoDir string) error {
	if strings.TrimSpace(repoDir) == "" {
		return fmt.Errorf("no repo set")
	}
	if _, err := runGit(ctx, repoDir, "rev-parse", "--is-inside-work-tree"); err != nil {
		return fmt.Errorf("not a git repo: %s", repoDir)
	}
	return nil
}

// ensureRepoInit is the push-side variant: if the path isn't a git repo yet it
// creates the dir and `git init`s it, so the user can name a fresh pool in the
// share dialog and move on. A remote-less repo just means push is skipped and
// the pool lives locally until a remote is added.
func ensureRepoInit(ctx context.Context, repoDir string) error {
	if strings.TrimSpace(repoDir) == "" {
		return fmt.Errorf("no repo set")
	}
	if _, err := runGit(ctx, repoDir, "rev-parse", "--is-inside-work-tree"); err == nil {
		return nil // already a repo
	}
	if err := os.MkdirAll(repoDir, 0o750); err != nil {
		return fmt.Errorf("create repo dir: %w", err)
	}
	if _, err := runGit(ctx, repoDir, "init"); err != nil {
		return err
	}
	return nil
}

// hasRemote reports whether repoDir has at least one configured remote — the
// gate for whether pushShared attempts a push at all.
func hasRemote(ctx context.Context, repoDir string) bool {
	out, err := runGit(ctx, repoDir, "remote")
	return err == nil && strings.TrimSpace(out) != ""
}
