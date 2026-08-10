package graph

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// seedCacheDir fakes a built graph: <base>/<name>/graph.db carrying meta.repo.
func seedCacheDir(t *testing.T, base, name, repo string) string {
	t.Helper()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "graph.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO meta (key, value) VALUES ('repo', ?)`, repo); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A cache dir whose source repo is gone is unreachable forever: the key is
// sha256(path), so nothing can ever look it up again. Worktree churn and
// agents naming temp dirs make these accumulate without bound.
func TestSweepOrphans_RemovesOnlyVanishedRepos(t *testing.T) {
	base := t.TempDir()
	live, err := canonicalRepo(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// The live entry must sit under its REAL key, or the sweep rightly reads it
	// as a cache nothing can reach.
	liveDir := seedCacheDir(t, base, cacheDirName(live), live)
	goneDir := seedCacheDir(t, base, "bbbbbbbbbbbb", filepath.Join(t.TempDir(), "deleted-worktree"))

	sweepOrphans(base)

	if _, err := os.Stat(liveDir); err != nil {
		t.Errorf("swept a cache whose repo still exists: %v", err)
	}
	if _, err := os.Stat(goneDir); !os.IsNotExist(err) {
		t.Errorf("orphaned cache survived the sweep (err=%v)", err)
	}
}

// A cache can be unreachable even though its repo path still exists: once
// canonicalRepo normalises subdirectories to the module root, an entry keyed on
// /repo/cmd is never looked up again. "Does the path exist" misses that, so the
// real test is whether the recorded repo still keys to THIS directory.
func TestSweepOrphans_RemovesReKeyedCache(t *testing.T) {
	base := t.TempDir()

	module := t.TempDir()
	if err := os.WriteFile(filepath.Join(module, "go.mod"), []byte("module example.com/m\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(module, "cmd")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}

	// A cache dir named for the pre-normalisation key, whose repo path is alive.
	stale := seedCacheDir(t, base, cacheDirName(sub), sub)
	// And the live one, keyed on the module root.
	canon, err := canonicalRepo(module)
	if err != nil {
		t.Fatal(err)
	}
	live := seedCacheDir(t, base, cacheDirName(canon), canon)

	sweepOrphans(base)

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("re-keyed cache survived: its repo exists but no longer keys here (err=%v)", err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Errorf("swept the live module-root cache: %v", err)
	}
}

// Never delete what cannot be proven orphaned. A dir with no graph.db, or a
// graph.db with no meta.repo, is left alone — it may be a build in flight.
func TestSweepOrphans_LeavesUnprovableDirs(t *testing.T) {
	base := t.TempDir()

	noDB := filepath.Join(base, "cccccccccccc")
	if err := os.MkdirAll(noDB, 0o750); err != nil {
		t.Fatal(err)
	}

	noMeta := filepath.Join(base, "dddddddddddd")
	if err := os.MkdirAll(noMeta, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(noMeta, "graph.db"), []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}

	sweepOrphans(base)

	for _, d := range []string{noDB, noMeta} {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("swept an unprovable dir %s: %v", filepath.Base(d), err)
		}
	}
}

// A successful build sweeps: builds are rare and already seconds long, so the
// sweep is free there, and a build is exactly when new cache dirs appear.
func TestBuild_SweepsOrphansOnSuccess(t *testing.T) {
	base := t.TempDir()
	repo, err := filepath.Abs("testdata/testmod")
	if err != nil {
		t.Fatal(err)
	}
	goneDir := seedCacheDir(t, base, "eeeeeeeeeeee", filepath.Join(t.TempDir(), "vanished"))

	m := NewManager(base)
	defer m.Close()

	if _, err := m.Package(context.Background(), PackageRequest{Repo: repo, Package: "testmod"}); err != nil {
		t.Fatalf("Package: %v", err)
	}

	if _, err := os.Stat(goneDir); !os.IsNotExist(err) {
		t.Errorf("a successful build did not sweep the orphan (err=%v)", err)
	}
}
