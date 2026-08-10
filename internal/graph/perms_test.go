package graph

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// graph.db stores up to maxSourceBytes of verbatim source per symbol for every
// repo indexed, including private ones. internal/db chmods mem.db to 0600 with
// the reasoning that the 0700 state dir "shields them today" but the files
// themselves hold unencrypted content — that argument applies at least as
// strongly here, and was never carried over. SQLite creates the file at the
// umask default, typically 0644.
func TestBuildIndex_GraphDBIsOwnerOnly(t *testing.T) {
	repo, err := filepath.Abs("testdata/testmod")
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "graphs", "abc123", "graph.db")

	if err := buildIndex(context.Background(), repo, dbPath, "stamp-perms"); err != nil {
		t.Fatalf("buildIndex: %v", err)
	}

	fi, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat graph.db: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("graph.db mode = %v, want 0600 — it holds source bodies", got)
	}
}

// sweepOrphans calls os.RemoveAll, so its guards are worth pinning explicitly.
// A symlink placed in the cache directory must never be traversed: os.ReadDir
// reports entry types without following links, so DirEntry.IsDir() is false for
// a symlink-to-directory and the entry is skipped. That behaviour is load
// bearing and invisible in the code, so assert it rather than assume it.
func TestSweepOrphans_DoesNotFollowSymlinks(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()

	// A cache dir OUTSIDE base whose repo is gone — i.e. it would be swept if
	// the sweep ever reached it through the link.
	victim := seedCacheDir(t, outside, "victimcache00", filepath.Join(t.TempDir(), "vanished"))
	if err := os.Symlink(victim, filepath.Join(base, "link-to-victim")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	sweepOrphans(context.Background(), base)

	if _, err := os.Stat(victim); err != nil {
		t.Errorf("sweep followed a symlink and deleted its target: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(base, "link-to-victim")); err != nil {
		t.Errorf("sweep removed the symlink entry itself: %v", err)
	}
}

// The cache directories themselves must not be group/world readable either —
// their names are repo-path hashes and their contents are source.
func TestBuildIndex_CacheDirIsNotWorldReadable(t *testing.T) {
	repo, err := filepath.Abs("testdata/testmod")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "graphs", "def456")
	if err := buildIndex(context.Background(), repo, filepath.Join(dir, "graph.db"), "stamp-dir"); err != nil {
		t.Fatalf("buildIndex: %v", err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm&fs.FileMode(0o007) != 0 {
		t.Errorf("cache dir mode = %v, want no world bits", perm)
	}
}
