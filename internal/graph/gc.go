package graph

import (
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// sweepOrphans deletes cache directories under base whose source repo no
// longer exists. The cache key is sha256(repo path), so once that path is gone
// the directory is unreachable forever — nothing can ever look it up again.
// Worktree churn and agents naming temp directories make these accumulate
// without bound; there is no other reclamation path.
//
// Deliberately conservative: a directory is removed only when its graph.db
// opens, yields meta.repo, and os.Stat on that path fails with precisely
// fs.ErrNotExist. A permission error, an I/O error, an unopenable db or a
// missing meta row all mean "cannot prove orphaned" and the directory is left
// alone — a build may even be in flight there.
//
// Safe against concurrent readers: on POSIX, unlinking a file another process
// holds open leaves that process's handle valid until it closes. Worst case
// for a false positive is a rebuild, because this is only ever a cache.
//
// Best-effort throughout — a sweep failure must never fail the build that
// triggered it.
func sweepOrphans(base string) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(base, e.Name())
		if orphanedCacheDir(dir) {
			_ = os.RemoveAll(dir)
		}
	}
}

// orphanedCacheDir reports whether dir is a built graph nothing can reach any
// more. False whenever that cannot be established.
//
// Two ways a cache becomes unreachable, and both must be caught:
//
//   - Its repo path is gone (deleted worktree, temp dir). Nothing will ever
//     hash to this directory again.
//   - Its repo path still exists but no longer keys HERE. canonicalRepo
//     normalises subdirectories to the module root, so an entry written when
//     an agent named /repo/cmd is now looked up under /repo's hash and the old
//     directory is dead weight. Checking only for a missing path would keep it
//     for ever.
//
// Stating the rule as "does this still key here" rather than "does the path
// exist" also means any future change to the key scheme reclaims its own
// leftovers automatically.
func orphanedCacheDir(dir string) bool {
	repo, ok := cachedRepoPath(filepath.Join(dir, "graph.db"))
	if !ok || repo == "" {
		return false
	}
	if _, err := os.Stat(repo); err != nil {
		// Only a definitive "not there" counts. An unmounted volume reports the
		// same, and that is accepted: losing a cache entry costs one rebuild.
		// A permission or I/O error proves nothing, so keep the directory.
		return errors.Is(err, fs.ErrNotExist)
	}
	canon, err := canonicalRepo(repo)
	if err != nil {
		return false // cannot prove anything — keep it
	}
	return cacheDirName(canon) != filepath.Base(dir)
}

// cachedRepoPath reads meta.repo out of a graph db, reporting whether it could
// be read at all. Opened read-only so a sweep can never mutate a live graph.
func cachedRepoPath(dbPath string) (string, bool) {
	if _, err := os.Stat(dbPath); err != nil {
		return "", false
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return "", false
	}
	defer db.Close()
	var repo string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key='repo'`).Scan(&repo); err != nil {
		return "", false
	}
	return repo, true
}
