package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// shareRepoFile holds the current Memory repo path (one line); knownReposFile
// is the tracked set the `share` flow offers.
const (
	shareRepoFile  = "share-repo"
	knownReposFile = "known-repos.json"
)

// KnownRepo is one tracked Memory repo (FR-4). remote is best-effort (empty if
// the repo has no origin); last_used orders the choices in the `share` flow.
type KnownRepo struct {
	Path     string `json:"path"`
	Remote   string `json:"remote,omitempty"`
	LastUsed int64  `json:"last_used"`
}

// ShareRepoPath resolves the current Memory repo (FR-4). Precedence:
//  1. DROIDS_MEM_SHARE_REPO env (tests/one-offs).
//  2. ~/.droids-mem/share-repo (persisted; load-bearing because the detached
//     boot-Fetch in `serve` does not inherit shell env — only a file reaches it).
//
// Returns "" (no error) when nothing is configured — the caller treats that as
// a clean no-op (boot Fetch) or a pointer-to-setup error (--repo alone).
func ShareRepoPath() (string, error) {
	if v := strings.TrimSpace(os.Getenv("DROIDS_MEM_SHARE_REPO")); v != "" {
		return v, nil
	}
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	// #nosec G304 -- path is Dir()/share-repo inside the trusted state dir.
	b, err := os.ReadFile(filepath.Join(dir, shareRepoFile))
	if err != nil {
		return "", nil //nolint:nilerr // a missing file means "not configured", not an error
	}
	return strings.TrimSpace(string(b)), nil
}

// SetShareRepo persists path as the current Memory repo and records it in the
// tracked set (last_used = now). remote is stored for display; pass "" if none.
func SetShareRepo(path, remote string) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, shareRepoFile), []byte(path+"\n"), 0o600); err != nil {
		return fmt.Errorf("write share-repo: %w", err)
	}
	return trackRepo(dir, KnownRepo{Path: path, Remote: remote, LastUsed: time.Now().Unix()})
}

// KnownRepos returns the tracked Memory repos, newest-used first. Missing file
// is an empty set, never an error.
func KnownRepos() ([]KnownRepo, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	return readKnownRepos(dir)
}

func readKnownRepos(dir string) ([]KnownRepo, error) {
	// #nosec G304 -- path is Dir()/known-repos.json inside the trusted state dir.
	b, err := os.ReadFile(filepath.Join(dir, knownReposFile))
	if err != nil {
		return nil, nil //nolint:nilerr // no tracked-set file yet means an empty set, not an error
	}
	var repos []KnownRepo
	if err := json.Unmarshal(b, &repos); err != nil {
		return nil, fmt.Errorf("parse known-repos: %w", err)
	}
	return repos, nil
}

// trackRepo upserts one repo by path and rewrites the tracked set sorted
// newest-used first.
func trackRepo(dir string, r KnownRepo) error {
	repos, err := readKnownRepos(dir)
	if err != nil {
		return err
	}
	out := make([]KnownRepo, 0, len(repos)+1)
	out = append(out, r)
	for _, ex := range repos {
		if ex.Path != r.Path {
			out = append(out, ex)
		}
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal known-repos: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, knownReposFile), b, 0o600); err != nil {
		return fmt.Errorf("write known-repos: %w", err)
	}
	return nil
}
