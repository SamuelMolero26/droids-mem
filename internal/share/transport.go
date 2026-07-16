// Package share is the git transport for the shared-context pool (PRD
// shared-context-sync, builds on ADR-0028). It moves scope='shared' memories
// between machines through a dedicated Memory repo — never a code repo — using
// git via os/exec (no new Go dependency, FR-7). The wire format, dedupe, and
// scrub are the store's; this package only routes the JSONL and drives git.
package share

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/samuelmolero26/droids-mem/internal/store"
)

// sharedFile is the per-project bucket name (FR-3): <repo>/<task_type>/shared.jsonl.
const sharedFile = "shared.jsonl"

// RetryableError marks a transport failure the caller should surface as
// retryable — chiefly a non-fast-forward push (FR-5): re-running Publish is the
// retry, since it is fetch-first and idempotent.
type RetryableError struct{ msg string }

func (e *RetryableError) Error() string { return e.msg }

// PublishResult summarizes one Publish (FR-8).
type PublishResult struct {
	Projects  int                `json:"projects"`  // buckets written
	Committed bool               `json:"committed"` // a commit was made (no-op tree commits nothing)
	Pushed    bool               `json:"pushed"`
	Import    store.ImportResult `json:"import"` // the pull-before-push merge (FR-5a)
}

// Fetch pulls the Memory repo and imports every <repo>/*/shared.jsonl into the
// local store (FR-6). git pull runs only when an upstream is configured (a
// local-only Memory repo just re-imports its files). The pull is bounded by
// ctx — boot-Fetch passes a timeout (PERF-2); a manual Fetch may pass an
// untimed ctx. Import is additive + dedupe-guarded, so re-Fetch is idempotent.
func Fetch(ctx context.Context, st *store.Store, repo string) (store.ImportResult, error) {
	var res store.ImportResult
	if hasUpstream(ctx, repo) {
		if _, err := git(ctx, repo, "pull", "--ff-only"); err != nil {
			return res, err
		}
	}
	matches, err := filepath.Glob(filepath.Join(repo, "*", sharedFile))
	if err != nil {
		return res, fmt.Errorf("glob pool: %w", err)
	}
	for _, path := range matches {
		f, err := os.Open(path) // #nosec G304 -- glob under the resolved repo root
		if err != nil {
			return res, fmt.Errorf("open %s: %w", path, err)
		}
		one, err := st.ImportShared(ctx, f)
		_ = f.Close()
		if err != nil {
			return res, err
		}
		res.Imported += one.Imported
		res.Skipped += one.Skipped
		res.Failed += one.Failed
	}
	return res, nil
}

// Publish is the fixed-order round-trip (FR-5): Fetch-first (data-loss guard,
// FR-5a), route the shared set into per-project buckets, commit, push. A
// non-fast-forward push returns a *RetryableError; re-running is the retry.
func Publish(ctx context.Context, st *store.Store, repo string) (PublishResult, error) {
	var res PublishResult

	// 1. Fetch-first: make the local shared set a superset of the remote so the
	//    full-file rewrite in step 2 only adds, never deletes a peer's lines.
	imp, err := Fetch(ctx, st, repo)
	if err != nil {
		return res, &RetryableError{msg: "pull before publish failed (" + err.Error() + "); run it again"}
	}
	res.Import = imp

	// 2. Route the single ExportShared stream into <repo>/<task_type>/shared.jsonl.
	var buf bytes.Buffer
	if err := st.ExportShared(ctx, &buf); err != nil {
		return res, fmt.Errorf("export: %w", err)
	}
	n, err := route(repo, buf.Bytes())
	if err != nil {
		return res, err
	}
	res.Projects = n

	// 3. Stage + commit. A no-change tree commits nothing and exits ok.
	if _, err := git(ctx, repo, "add", "-A"); err != nil {
		return res, err
	}
	staged, err := hasStaged(ctx, repo)
	if err != nil {
		return res, err
	}
	if !staged {
		return res, nil // nothing to commit or push
	}
	if _, err := git(ctx, repo, "commit", "-m", "droids-mem: publish shared memory"); err != nil {
		return res, err
	}
	res.Committed = true

	// 4. Push. First push on a fresh repo sets the upstream.
	pushArgs := []string{"push"}
	if !hasUpstream(ctx, repo) {
		pushArgs = []string{"push", "-u", "origin", "HEAD"}
	}
	if out, err := git(ctx, repo, pushArgs...); err != nil {
		if isNonFastForward(out) {
			return res, &RetryableError{msg: "Memory repo remote moved; run it again"}
		}
		return res, err
	}
	res.Pushed = true
	return res, nil
}

// route buckets the ExportShared JSONL stream by task_type and rewrites each
// <repo>/<task_type>/shared.jsonl (full byte-stable rewrite; export is ORDER BY
// fingerprint). Raw lines are preserved verbatim — the decode only reads
// task_type for routing. SEC-2: every target path is confirmed to stay under
// the repo root before any write. Returns the number of buckets written.
func route(repo string, export []byte) (int, error) {
	root, err := filepath.Abs(repo)
	if err != nil {
		return 0, fmt.Errorf("resolve repo: %w", err)
	}
	buckets := map[string]*bytes.Buffer{}
	br := bufio.NewReader(bytes.NewReader(export))
	for {
		raw, readErr := br.ReadBytes('\n')
		if line := bytes.TrimSpace(raw); len(line) > 0 {
			var m struct {
				TaskType string `json:"task_type"`
			}
			if json.Unmarshal(line, &m) == nil && m.TaskType != "" {
				b := buckets[m.TaskType]
				if b == nil {
					b = &bytes.Buffer{}
					buckets[m.TaskType] = b
				}
				b.Write(line)
				b.WriteByte('\n')
			}
		}
		if readErr != nil {
			break // io.EOF or a read fault; either way the buffer is fully drained
		}
	}

	written := 0
	for tt, b := range buckets {
		dir := filepath.Join(root, tt)
		// SEC-2: the joined path must stay under the repo root. task_type is
		// already SEC-1-gated, but the writer never trusts upstream validation.
		if !contained(root, dir) {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil { // #nosec G301 -- shared, non-secret pool dir
			return written, fmt.Errorf("mkdir %s: %w", dir, err)
		}
		// #nosec G306 -- shared pool file is git-tracked and non-secret by design (scrub ran on save).
		if err := os.WriteFile(filepath.Join(dir, sharedFile), b.Bytes(), 0o644); err != nil {
			return written, fmt.Errorf("write %s: %w", dir, err)
		}
		written++
	}
	return written, nil
}

// contained reports whether child resolves to a path under root (SEC-2).
func contained(root, child string) bool {
	rel, err := filepath.Rel(root, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// git runs `git -C <repo> <args...>` (SEC-3: fixed argv, no shell, repo via -C).
// Returns combined output so callers can inspect it (e.g. non-fast-forward).
func git(ctx context.Context, repo string, args ...string) ([]byte, error) {
	full := append([]string{"-C", repo}, args...)
	// #nosec G204 -- fixed "git" binary, fixed argv slice, repo passed via -C
	// (SEC-3); no shell, no path concatenation.
	out, err := exec.CommandContext(ctx, "git", full...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return out, nil
}

// hasUpstream reports whether the current branch tracks a remote branch.
func hasUpstream(ctx context.Context, repo string) bool {
	_, err := git(ctx, repo, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	return err == nil
}

// hasStaged reports whether `git add -A` staged any change.
func hasStaged(ctx context.Context, repo string) (bool, error) {
	out, err := git(ctx, repo, "diff", "--cached", "--name-only")
	if err != nil {
		return false, err
	}
	return len(bytes.TrimSpace(out)) > 0, nil
}

func isNonFastForward(out []byte) bool {
	s := string(out)
	return strings.Contains(s, "non-fast-forward") || strings.Contains(s, "[rejected]")
}

// IsGitRepo reports whether path is inside a git work tree (FR-4 adopt gate).
func IsGitRepo(path string) bool {
	out, err := exec.Command("git", "-C", path, "rev-parse", "--is-inside-work-tree").Output() // #nosec G204 -- fixed argv, path via -C
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// RemoteURL returns origin's URL, or "" if none (best-effort, for tracking).
func RemoteURL(path string) string {
	out, err := exec.Command("git", "-C", path, "remote", "get-url", "origin").Output() // #nosec G204 -- fixed argv, path via -C
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
