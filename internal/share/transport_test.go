package share

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/samuelmolero26/droids-mem/internal/db"
	"github.com/samuelmolero26/droids-mem/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Init(conn); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return store.New(conn)
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
}

func gitInitClone(t *testing.T, origin, dst string) {
	t.Helper()
	run(t, filepath.Dir(dst), "git", "clone", origin, dst)
	run(t, dst, "git", "config", "user.email", "test@example.com")
	run(t, dst, "git", "config", "user.name", "test")
}

func saveShared(t *testing.T, s *store.Store, taskType, title string) {
	t.Helper()
	_, err := s.Save(context.Background(), store.SaveRequest{
		TaskType: taskType,
		Kind:     "task_pattern",
		Title:    title,
		What:     "context for " + title,
		Learned:  "lesson " + title,
		Scope:    "shared",
	})
	if err != nil {
		t.Fatalf("save %q: %v", title, err)
	}
}

// TestPublishFetch_RoundTrip proves A4: machine A Publishes, machine B Fetches
// the pool via a shared bare origin, and B's store holds A's lessons. A re-Fetch
// is idempotent (dedupe holds).
func TestPublishFetch_RoundTrip(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	ctx := context.Background()
	tmp := t.TempDir()

	origin := filepath.Join(tmp, "origin.git")
	run(t, tmp, "git", "init", "--bare", "-b", "main", origin)

	// Machine A: two projects, publish.
	a := filepath.Join(tmp, "a")
	gitInitClone(t, origin, a)
	storeA := newStore(t)
	saveShared(t, storeA, "proj_one", "alpha")
	saveShared(t, storeA, "proj_two", "beta")

	res, err := Publish(ctx, storeA, a)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if !res.Pushed || res.Projects != 2 {
		t.Fatalf("publish result = %+v, want pushed with 2 projects", res)
	}
	// FR-3: one shared.jsonl per task_type.
	for _, tt := range []string{"proj_one", "proj_two"} {
		if _, err := os.Stat(filepath.Join(a, tt, "shared.jsonl")); err != nil {
			t.Fatalf("missing bucket for %s: %v", tt, err)
		}
	}

	// Machine B: clone the now-populated repo and Fetch.
	b := filepath.Join(tmp, "b")
	gitInitClone(t, origin, b)
	storeB := newStore(t)
	imp, err := Fetch(ctx, storeB, b)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if imp.Imported != 2 {
		t.Fatalf("fetch imported = %d, want 2", imp.Imported)
	}
	if n, _ := storeB.CountShared(ctx); n != 2 {
		t.Fatalf("B shared count = %d, want 2", n)
	}

	// Re-Fetch is idempotent: dedupe skips the already-held rows.
	imp2, err := Fetch(ctx, storeB, b)
	if err != nil {
		t.Fatalf("re-fetch: %v", err)
	}
	if imp2.Imported != 0 || imp2.Skipped != 2 {
		t.Fatalf("re-fetch = %+v, want imported=0 skipped=2", imp2)
	}
}

// TestPublish_FetchFirstPreservesPeerLines proves the FR-5a data-loss guard:
// two machines publish different memories into the SAME project. B never saw
// A's push, but Publish is fetch-first, so B pulls+imports A's line before its
// full-file rewrite of proj_one/shared.jsonl — both lines survive on origin.
func TestPublish_FetchFirstPreservesPeerLines(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	ctx := context.Background()
	tmp := t.TempDir()

	origin := filepath.Join(tmp, "origin.git")
	run(t, tmp, "git", "init", "--bare", "-b", "main", origin)

	// A sets up the pool with one memory in proj_one.
	a := filepath.Join(tmp, "a")
	gitInitClone(t, origin, a)
	storeA := newStore(t)
	saveShared(t, storeA, "proj_one", "alpha")
	if _, err := Publish(ctx, storeA, a); err != nil {
		t.Fatalf("A publish: %v", err)
	}

	// B clones the populated pool and publishes a DIFFERENT memory into the same
	// project. Fetch-first must fold in A's line, not overwrite it.
	b := filepath.Join(tmp, "b")
	gitInitClone(t, origin, b)
	storeB := newStore(t)
	saveShared(t, storeB, "proj_one", "beta")
	resB, err := Publish(ctx, storeB, b)
	if err != nil {
		t.Fatalf("B publish: %v", err)
	}
	if !resB.Pushed {
		t.Fatalf("B publish not pushed: %+v", resB)
	}
	if resB.Import.Imported != 1 {
		t.Fatalf("B fetch-first import = %d, want 1 (A's line)", resB.Import.Imported)
	}

	// Origin's final bucket must hold BOTH memories — A's line survived B's rewrite.
	check := filepath.Join(tmp, "check")
	gitInitClone(t, origin, check)
	data, err := os.ReadFile(filepath.Join(check, "proj_one", sharedFile))
	if err != nil {
		t.Fatalf("read final bucket: %v", err)
	}
	for _, want := range []string{"alpha", "beta"} {
		if !bytes.Contains(data, []byte(want)) {
			t.Fatalf("final bucket lost %q (data-loss guard failed):\n%s", want, data)
		}
	}
	if n := bytes.Count(bytes.TrimSpace(data), []byte("\n")) + 1; n != 2 {
		t.Fatalf("final bucket has %d lines, want 2:\n%s", n, data)
	}
}

// TestPublish_DivergedRemoteIsRetryable proves FR-5: when the Memory repo has
// moved under a diverged local history, Publish's fetch-first (--ff-only) fails
// and surfaces a *RetryableError — re-running is the retry.
func TestPublish_DivergedRemoteIsRetryable(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	ctx := context.Background()
	tmp := t.TempDir()

	origin := filepath.Join(tmp, "origin.git")
	run(t, tmp, "git", "init", "--bare", "-b", "main", origin)

	// Establish the pool so both clones track origin/main.
	a := filepath.Join(tmp, "a")
	gitInitClone(t, origin, a)
	storeA := newStore(t)
	saveShared(t, storeA, "proj_one", "alpha")
	if _, err := Publish(ctx, storeA, a); err != nil {
		t.Fatalf("setup publish: %v", err)
	}

	b := filepath.Join(tmp, "b")
	gitInitClone(t, origin, b)
	storeB := newStore(t)
	saveShared(t, storeB, "proj_two", "beta")

	// Origin advances under B (A publishes again), and B grows a divergent local
	// commit not on origin — now B and origin have forked histories.
	saveShared(t, storeA, "proj_three", "gamma")
	if _, err := Publish(ctx, storeA, a); err != nil {
		t.Fatalf("advance publish: %v", err)
	}
	run(t, b, "git", "commit", "--allow-empty", "-m", "local divergence")

	// B's fetch-first pull can't fast-forward → retryable.
	var re *RetryableError
	if _, err := Publish(ctx, storeB, b); !errors.As(err, &re) {
		t.Fatalf("diverged publish err = %v, want *RetryableError", err)
	}
}

// TestRoute_ContainmentRejectsEscape proves SEC-2: a router line whose
// task_type would escape the repo root is skipped — nothing is written outside.
func TestRoute_ContainmentRejectsEscape(t *testing.T) {
	repo := t.TempDir()
	// A hand-crafted export line with a traversal task_type (SEC-1 would reject
	// it on import, but the router never trusts upstream validation).
	line := []byte(`{"task_type":"../escape","kind":"task_pattern","title":"x","what":"y","learned":"z","tags":""}` + "\n")
	n, err := route(repo, line)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if n != 0 {
		t.Fatalf("wrote %d buckets, want 0 (containment)", n)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(repo), "escape")); err == nil {
		t.Fatalf("traversal path was written outside repo root")
	}
}
