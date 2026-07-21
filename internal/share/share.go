// Package share is the git transport for the shared-memory pool (ADR-0028,
// ADR-0029). It shells git over a dedicated Memory repo — never a code repo —
// to Publish the local scope='shared' set and Fetch a teammate's. It owns no
// history or merge logic (git does); the store owns scrub, dedupe, and the
// wire format. Extracted from the TUI so `serve` can boot-Fetch headless
// (ADR-0029 §5).
package share

import (
	"bytes"
	"context"
	"fmt"
	"io"
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

// Store is the narrow store surface the transport needs: export the local
// shared pool out, import a pulled pool in. *store.Store satisfies it in
// production; the TUI's memStore (a superset) satisfies it too.
type Store interface {
	ExportShared(context.Context, io.Writer) error
	ImportShared(context.Context, io.Reader) (store.ImportResult, error)
}

// runGit runs `git -C dir args...`, returning combined output. Git writes most
// of its useful diagnostics (auth failure, dirty tree, no upstream) to stderr,
// so we fold both streams into the error the caller surfaces.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	// #nosec G204 -- command is the constant "git"; dir is the user's own repo
	// path and args are all hardcoded call-site literals, none attacker-supplied.
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	// GIT_TERMINAL_PROMPT=0: never prompt for credentials on the controlling
	// terminal — a prompt would hang a detached serve or garble the TUI. Auth
	// failure comes back as a clean error instead.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(out.String()))
	}
	return out.String(), nil
}

// Push publishes the whole scope='shared' pool into repoDir/shared.jsonl and
// pushes it (ADR-0028, ADR-0029). The repo is created when the path isn't one
// yet (guarded so a pool is never nested inside a code repo), and a from-nothing
// pool is turned into a real GitHub repo in the user's account so it lands in
// their repo list. Nothing to commit is a no-op, not an error.
//
// Fetch-first (ADR-0029 §4, data-loss guard): when a remote exists we pull +
// import it first so the local shared set is a SUPERSET before the full-file
// rewrite — otherwise the rewrite would drop teammate rows we don't hold and
// the push would delete them from the pool.
func Push(ctx context.Context, repoDir string, s Store, n int) error {
	if err := ensureRepoInit(ctx, repoDir); err != nil {
		return err
	}
	if hasRemote(ctx, repoDir) {
		if _, err := fetchInto(ctx, repoDir, s); err != nil {
			return err
		}
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
	noun := "memories"
	if n == 1 {
		noun = "memory"
	}
	msg := fmt.Sprintf("share: %d %s", n, noun)
	if out, err := runGit(ctx, repoDir, "commit", "-m", msg); err != nil {
		if strings.Contains(out, "nothing to commit") {
			return nil // pool already up to date — not a failure
		}
		return err
	}
	if !hasRemote(ctx, repoDir) {
		// Freshly-created local pool: publish it as a real GitHub repo so it
		// lands in the user's repo list (ADR-0029 §3), origin set + pushed.
		return publishNewRepo(ctx, repoDir)
	}
	if _, err := runGit(ctx, repoDir, "push"); err != nil {
		return err
	}
	return nil
}

// Fetch pulls repoDir then imports its shared.jsonl into the local store (the
// consume half of the pool, ADR-0028). Used by the TUI Pull action and by the
// boot auto-Fetch in serve (ADR-0029 §5).
func Fetch(ctx context.Context, repoDir string, s Store) (store.ImportResult, error) {
	if err := ensureRepo(ctx, repoDir); err != nil {
		return store.ImportResult{}, err
	}
	return fetchInto(ctx, repoDir, s)
}

// fetchInto pulls repoDir and imports its shared.jsonl into s. Shared by Fetch
// and Push's fetch-first guard. Import is resilient per-row (ADR-0028), so a
// poisoned line is counted in Failed, not fatal. A pool with no file yet (fresh
// remote) is not an error.
func fetchInto(ctx context.Context, repoDir string, s Store) (store.ImportResult, error) {
	if _, err := runGit(ctx, repoDir, "pull"); err != nil {
		return store.ImportResult{}, err
	}
	path := filepath.Join(repoDir, sharedFile)
	// #nosec G304 -- repoDir is a user-chosen path they own.
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return store.ImportResult{}, nil // remote pool has no file yet
		}
		return store.ImportResult{}, fmt.Errorf("open %s: %w", sharedFile, err)
	}
	defer f.Close()
	return s.ImportShared(ctx, f)
}

// ensureRepo rejects a path that isn't a git work tree, so the caller reports
// "not a git repo" instead of a cryptic add/commit failure. Used by Fetch —
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
// creates the dir and `git init`s it, so the user can name a fresh pool and move
// on. The Memory pool must be its OWN repo — never nested inside a code repo,
// where it would ship into clones/deploy artifacts (ADR-0029 §1) — so creation
// is refused when the target sits inside another git work tree.
func ensureRepoInit(ctx context.Context, repoDir string) error {
	if strings.TrimSpace(repoDir) == "" {
		return fmt.Errorf("no repo set")
	}
	if _, err := runGit(ctx, repoDir, "rev-parse", "--is-inside-work-tree"); err == nil {
		return nil // already a repo
	}
	if err := notInsideWorkTree(ctx, repoDir); err != nil {
		return err
	}
	if err := os.MkdirAll(repoDir, 0o750); err != nil {
		return fmt.Errorf("create repo dir: %w", err)
	}
	if _, err := runGit(ctx, repoDir, "init"); err != nil {
		return err
	}
	return nil
}

// notInsideWorkTree errors if repoDir would be created inside an existing git
// work tree (its code repo) — the guard behind "Memory repo is a separate repo,
// not a folder in your project" (ADR-0029 §1). repoDir itself may not exist yet,
// so it walks up to the nearest existing ancestor and asks git about that.
func notInsideWorkTree(ctx context.Context, repoDir string) error {
	dir := filepath.Dir(repoDir)
	for {
		if _, err := os.Stat(dir); err == nil {
			break // nearest existing ancestor
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil // reached filesystem root, nothing exists above
		}
		dir = parent
	}
	if out, err := runGit(ctx, dir, "rev-parse", "--show-toplevel"); err == nil {
		return fmt.Errorf("refusing to create Memory repo inside working repo %s — pick a path outside any code repo", strings.TrimSpace(out))
	}
	return nil
}

// publishNewRepo turns the freshly-committed local pool into a real GitHub repo
// in the user's account (ADR-0029 §3) so it shows up in their repo list, with
// origin set and the first commit pushed. Shells `gh`; if gh is missing or
// unauthenticated the pool stays a valid local repo and we return a clear,
// actionable message (the local share already succeeded).
func publishNewRepo(ctx context.Context, repoDir string) error {
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("committed locally; install `gh` (or add a remote) to publish %q to GitHub", filepath.Base(repoDir))
	}
	name := filepath.Base(repoDir)
	// #nosec G204 -- "gh" is constant; name/repoDir are the user's own chosen path.
	cmd := exec.CommandContext(ctx, "gh", "repo", "create", name,
		"--private", "--source", repoDir, "--remote", "origin", "--push")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gh repo create: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// hasRemote reports whether repoDir has at least one configured remote — the
// gate for whether Push pushes vs. publishes a new GitHub repo.
func hasRemote(ctx context.Context, repoDir string) bool {
	out, err := runGit(ctx, repoDir, "remote")
	return err == nil && strings.TrimSpace(out) != ""
}
