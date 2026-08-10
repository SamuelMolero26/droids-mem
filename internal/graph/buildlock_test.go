package graph

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// The in-process build lock is a field on *Manager, so it coalesces builds
// within one process only. With stdio transports every agent session is its own
// process, and N sessions querying one stale repo each ran a full go/packages
// type-check of the same tree. A file lock coalesces across processes: the
// second arrival waits, then finds the first one's result already on disk.
//
// This is the payoff test — the wait is worthless if the waiter rebuilds anyway.
func TestBuildIndexLocked_SkipsBuildWhenAnotherLanded(t *testing.T) {
	repo, err := filepath.Abs("testdata/testmod")
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "graph.db")

	want, err := stamp(repo)
	if err != nil {
		t.Fatal(err)
	}

	// First arrival builds.
	before := buildStarts.Load()
	if err := buildIndexLocked(context.Background(), repo, dbPath, want); err != nil {
		t.Fatalf("first build: %v", err)
	}
	if got := buildStarts.Load() - before; got != 1 {
		t.Fatalf("first call ran %d builds, want 1", got)
	}

	// Second arrival, same stamp: the graph on disk already satisfies it.
	before = buildStarts.Load()
	if err := buildIndexLocked(context.Background(), repo, dbPath, want); err != nil {
		t.Fatalf("second build: %v", err)
	}
	if got := buildStarts.Load() - before; got != 0 {
		t.Errorf("second call ran %d builds, want 0 — the post-lock freshness re-check did not fire", got)
	}
}

// A stamp the on-disk graph does not carry must still build: the re-check must
// compare stamps, not merely notice that some graph exists.
func TestBuildIndexLocked_BuildsWhenStampDiffers(t *testing.T) {
	repo, err := filepath.Abs("testdata/testmod")
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "graph.db")

	if err := buildIndexLocked(context.Background(), repo, dbPath, "stamp-one"); err != nil {
		t.Fatalf("seed build: %v", err)
	}
	before := buildStarts.Load()
	if err := buildIndexLocked(context.Background(), repo, dbPath, "stamp-two"); err != nil {
		t.Fatalf("second build: %v", err)
	}
	if got := buildStarts.Load() - before; got != 1 {
		t.Errorf("differing stamp ran %d builds, want 1", got)
	}
}

// Waiting on the lock must be abandonable. A cold build can run for seconds and
// the whole point of ctx here is that a caller's deadline stays enforceable —
// a blocking flock would strand the goroutine past its deadline.
func TestAcquireBuildLock_RespectsContext(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "graph.db")

	release, err := acquireBuildLock(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	release2, err := acquireBuildLock(ctx, dbPath)
	if err == nil {
		release2()
		t.Fatal("second acquire succeeded while the lock was held")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("second acquire took %v to give up — it is not polling the context", elapsed)
	}
}

// Release must actually free the lock, or one build would wedge the repo for
// the life of the process.
func TestAcquireBuildLock_ReleaseFreesIt(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "graph.db")

	release, err := acquireBuildLock(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	release()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	release2, err := acquireBuildLock(ctx, dbPath)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	release2()
}
