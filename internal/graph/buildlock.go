package graph

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

// buildLockFile sits beside graph.db and carries no content — flock state lives
// in the kernel, keyed on the open file description.
const buildLockFile = ".build.lock"

// buildLockPoll is how often a waiter retries. A cold build runs for seconds,
// so polling this often costs nothing measurable and keeps the wait
// abandonable: a blocking flock cannot be interrupted by a context.
const buildLockPoll = 25 * time.Millisecond

// acquireBuildLock takes an exclusive advisory lock on the repo's cache
// directory, or gives up when ctx dies. The returned release func is always
// non-nil on success and must be called exactly once.
//
// Manager.locks only coalesces builds inside ONE process. Under stdio every
// agent session is a separate process, so N sessions hitting one stale repo
// each ran a full go/packages type-check of the same tree. This lock makes them
// queue instead; buildIndexLocked then turns queueing into coalescing by
// re-checking freshness once it holds the lock.
//
// Polled with LOCK_NB rather than blocking: a blocking flock cannot be
// cancelled, so a caller's deadline would stop being enforceable, and parking a
// goroutine on it would leak that goroutine past the deadline.
//
// The kernel drops the lock when the fd closes, including on crash, so a dead
// builder can never wedge a repo.
func acquireBuildLock(ctx context.Context, dbPath string) (func(), error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}
	path := filepath.Join(dir, buildLockFile)
	// #nosec G304 -- path is the state dir's cache subdir plus a constant name.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open build lock: %w", err)
	}

	release := func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}

	timer := time.NewTimer(buildLockPoll)
	defer timer.Stop()
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return release, nil
		}
		// EWOULDBLOCK means "held by someone else"; anything else is a real
		// failure. EAGAIN aliases EWOULDBLOCK on Linux and Darwin, so one
		// comparison covers both.
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-timer.C:
			timer.Reset(buildLockPoll)
		}
	}
}

// buildIndexLocked builds the graph under the cross-process build lock, but
// only if nobody else already produced the wanted stamp while we waited.
//
// The re-check is the whole point: without it the lock would merely serialise N
// identical builds instead of collapsing them to one. It reads the stamp
// straight off disk rather than through Manager's handle cache, because the
// file we care about was just replaced by a DIFFERENT process — any handle this
// process holds still points at the pre-rename inode.
//
// A lock failure is not fatal. Falling through to an unlocked build restores
// exactly the previous behaviour (duplicate work, correct result) rather than
// failing a query over a coalescing optimisation.
func buildIndexLocked(ctx context.Context, repo, dbPath, stampVal string) error {
	release, err := acquireBuildLock(ctx, dbPath)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return buildIndex(ctx, repo, dbPath, stampVal)
	}
	defer release()

	if stampOnDisk(dbPath) == stampVal {
		return nil // another process built exactly this while we waited
	}
	return buildIndex(ctx, repo, dbPath, stampVal)
}

// stampOnDisk reads meta.stamp directly from the graph db, bypassing every
// cached handle. Returns "" when the file is absent, unopenable, or has no
// stamp — all of which mean "not the graph we want".
func stampOnDisk(dbPath string) string {
	if _, err := os.Stat(dbPath); err != nil {
		return ""
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return ""
	}
	defer db.Close()
	var s string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key='stamp'`).Scan(&s); err != nil {
		return ""
	}
	return s
}
