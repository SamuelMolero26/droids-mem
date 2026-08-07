package graph

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// copyFixture copies testdata/testmod into a fresh temp dir so a test can
// mutate the tree without touching the shared fixture.
func copyFixture(t *testing.T) string {
	t.Helper()
	src, err := filepath.Abs("testdata/testmod")
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "testmod")
	// Recursive: the fixture has a zz/ subpackage, and a partial copy fails to
	// type-check rather than failing the assertion under test.
	err = filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

// managerFor returns a Manager rooted at a temp graph dir, with stamp caching
// disabled so a mutation is visible to the very next query.
func managerFor(t *testing.T) *Manager {
	t.Helper()
	old := stampTTL
	stampTTL = 0
	t.Cleanup(func() { stampTTL = old })

	m := NewManager(filepath.Join(t.TempDir(), "graphs"))
	t.Cleanup(m.Close)
	return m
}

func writeFile(t *testing.T, repo, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// waitForBuild blocks until no build is tracked for repo, or the deadline hits.
// It never launches work — it only observes m.builds.
func waitForBuild(t *testing.T, m *Manager, repo string, d time.Duration) {
	t.Helper()
	canon, err := canonicalRepo(repo)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		m.buildsMu.Lock()
		bs, ok := m.builds[canon]
		m.buildsMu.Unlock()
		if !ok {
			return
		}
		select {
		case <-bs.done:
			return
		case <-time.After(5 * time.Millisecond):
		}
	}
	t.Fatalf("build for %s did not finish within %s", repo, d)
}

// --- D1: a completing rebuild must not close the handle its caller is using ---

// TestEnsureFresh_HandleSurvivesRebuild pins the use-after-close defect: the
// warm-serve path hands the caller a cached *sql.DB and then buildAsync's
// closeConn used to Close that very handle when the build landed, so a query
// still assembling its response failed with "sql: database is closed".
func TestEnsureFresh_HandleSurvivesRebuild(t *testing.T) {
	repo := copyFixture(t)
	m := managerFor(t)
	ctx := context.Background()

	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatal(err)
	}

	// Move the stamp with a file that still type-checks, so the rebuild succeeds
	// and reaches closeConn (the failure path never closes anything).
	writeFile(t, repo, "added.go", "package main\n\nfunc AddedSymbol() {}\n")

	conn, release, fresh, err := m.ensureFresh(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if !fresh.Stale {
		t.Fatalf("expected a stale warm-serve, got %+v", fresh)
	}

	// Let the async rebuild finish while we still hold the handle.
	waitForBuild(t, m, repo, 30*time.Second)

	var n int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM symbols`).Scan(&n); err != nil {
		t.Fatalf("held handle broke after the rebuild landed: %v", err)
	}
	if n == 0 {
		t.Fatal("held handle returned an empty graph")
	}
}

// TestEnsureFresh_ReleasePicksUpNewGraph guards the other half: once every
// caller has released, the next query must observe the rebuilt graph rather
// than the retained old handle.
func TestEnsureFresh_ReleasePicksUpNewGraph(t *testing.T) {
	repo := copyFixture(t)
	m := managerFor(t)
	ctx := context.Background()

	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "added.go", "package main\n\nfunc AddedSymbol() {}\n")

	if _, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "Announce"}); err != nil {
		t.Fatal(err)
	}
	waitForBuild(t, m, repo, 30*time.Second)

	resp, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "AddedSymbol"})
	if err != nil {
		t.Fatalf("new symbol not visible after rebuild: %v", err)
	}
	if resp.Freshness.Stale {
		t.Errorf("expected fresh after a successful rebuild, got %+v", resp.Freshness)
	}
	if resp.Symbol == nil || resp.Symbol.QName != "testmod.AddedSymbol" {
		t.Fatalf("wrong symbol: %+v", resp.Symbol)
	}
}

// --- D2: a superseded build must close its done channel ---

// TestSupersededBuild_ClosesDone pins the orphaned-channel defect: a WaitBuild
// caller already blocked on the superseded state's done channel used to wait
// its full timeout even though the newer build finished in milliseconds.
func TestSupersededBuild_ClosesDone(t *testing.T) {
	repo := copyFixture(t)
	m := managerFor(t)
	canon, err := canonicalRepo(repo)
	if err != nil {
		t.Fatal(err)
	}

	// The repo must be stale for the warm-serve branch to run at all.
	if _, err := m.Index(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "added.go", "package main\n\nfunc AddedSymbol() {}\n")

	// A never-finishing build with an outdated stamp occupies the slot, so the
	// next ensureFresh takes the supersede branch deterministically (no real
	// build to race against).
	bctx, bcancel := context.WithCancel(context.Background())
	stuck := &buildState{ctx: bctx, cancel: bcancel, stamp: "STALE-STAMP", done: make(chan struct{})}
	m.buildsMu.Lock()
	m.builds[canon] = stuck
	m.buildsMu.Unlock()

	// Any query now sees a build with a mismatched stamp and must supersede it.
	if _, err := m.Symbol(context.Background(), SymbolRequest{Repo: repo, Symbol: "Announce"}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-stuck.done:
	case <-time.After(2 * time.Second):
		t.Fatal("superseded build's done channel was never closed")
	}
	if bctx.Err() == nil {
		t.Error("superseded build's context was not cancelled")
	}
	waitForBuild(t, m, repo, 30*time.Second)
}

// --- issue #73 case 1 + D3: WaitBuild with no build in flight ---

func TestWaitBuild_NoBuild_FreshRepo(t *testing.T) {
	repo := copyFixture(t)
	m := managerFor(t)
	ctx := context.Background()

	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatal(err)
	}

	resp, err := m.WaitBuild(ctx, repo, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Completed || !resp.Rebuilt {
		t.Fatalf("want completed+rebuilt on a fresh repo, got %+v", resp)
	}
	if resp.Freshness.Stamp == "" {
		t.Error("freshness stamp should be populated from the real graph")
	}
	m.buildsMu.Lock()
	n := len(m.builds)
	m.buildsMu.Unlock()
	if n != 0 {
		t.Errorf("WaitBuild launched %d build(s) against a fresh repo", n)
	}
}

// TestWaitBuild_StaleRepo_WaitsForTheBuildItStarted pins D3: WaitBuild used to
// take the "no active build" branch, let ensureFresh launch a rebuild, and
// immediately report Completed:true — claiming success for work that had just
// begun. It must instead wait for that build within the timeout.
func TestWaitBuild_StaleRepo_WaitsForTheBuildItStarted(t *testing.T) {
	repo := copyFixture(t)
	m := managerFor(t)
	ctx := context.Background()

	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "added.go", "package main\n\nfunc AddedSymbol() {}\n")

	resp, err := m.WaitBuild(ctx, repo, 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Completed || !resp.Rebuilt {
		t.Fatalf("want completed+rebuilt after waiting, got %+v", resp)
	}
	if resp.Freshness.Stale {
		t.Errorf("freshness should be fresh once the build landed: %+v", resp.Freshness)
	}
	// The graph really must contain the new symbol, not just claim freshness.
	if _, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "AddedSymbol"}); err != nil {
		t.Fatalf("WaitBuild reported rebuilt but the new symbol is missing: %v", err)
	}
}

// TestWaitBuild_ColdRepo_RespectsTimeout pins the other half of D3: with no
// graph at all, the "no active build" branch ran a *synchronous* cold build and
// ignored the timeout argument entirely.
func TestWaitBuild_ColdRepo_RespectsTimeout(t *testing.T) {
	repo := copyFixture(t)
	m := managerFor(t)

	start := time.Now()
	resp, err := m.WaitBuild(context.Background(), repo, 1*time.Millisecond)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("a timeout must be reported in the response, not as an error: %v", err)
	}
	if resp.Completed {
		t.Errorf("cold build cannot complete within 1ms, got %+v", resp)
	}
	if elapsed > 5*time.Second {
		t.Errorf("WaitBuild ignored its timeout: took %s", elapsed)
	}
}

// --- issue #73 case 3 + D7: the timeout branch ---

// TestWaitBuild_Timeout uses an injected build whose done channel never closes.
// A type-check-failing repo does NOT reach this branch: packages.Load fails
// fast, so the build *completes* with an error and WaitBuild returns
// Completed:true.
func TestWaitBuild_Timeout(t *testing.T) {
	repo := copyFixture(t)
	m := managerFor(t)
	ctx := context.Background()

	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatal(err)
	}
	canon, err := canonicalRepo(repo)
	if err != nil {
		t.Fatal(err)
	}

	// Make the repo stale, then occupy the build slot with a never-finishing
	// build carrying the CURRENT stamp: ensureFresh then attaches to it rather
	// than superseding it, and WaitBuild has something real to block on.
	writeFile(t, repo, "added.go", "package main\n\nfunc AddedSymbol() {}\n")
	current, err := stamp(repo)
	if err != nil {
		t.Fatal(err)
	}

	// Real cancel func: Manager.Close cancels every tracked build on cleanup.
	bctx, bcancel := context.WithCancel(context.Background())
	defer bcancel()
	m.buildsMu.Lock()
	m.builds[canon] = &buildState{ctx: bctx, cancel: bcancel, stamp: current, done: make(chan struct{})}
	m.buildsMu.Unlock()
	defer func() {
		m.buildsMu.Lock()
		delete(m.builds, canon)
		m.buildsMu.Unlock()
	}()

	resp, err := m.WaitBuild(ctx, repo, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Completed {
		t.Fatalf("want completed=false on timeout, got %+v", resp)
	}
	if !resp.Freshness.Stale || !resp.Freshness.Rebuilding {
		t.Errorf("timeout must report stale+rebuilding, got %+v", resp.Freshness)
	}
	// D7: the timeout branch used to fabricate an empty Freshness, hiding the
	// perfectly valid stamp of the graph still being served.
	if resp.Freshness.Stamp == "" {
		t.Error("timeout response dropped the real stamp of the served graph")
	}
}

// --- issue #73 case 4: buildAsync success vs failure ---

func TestBuildAsync_FailureServesStaleAndRecordsError(t *testing.T) {
	repo := copyFixture(t)
	m := managerFor(t)
	ctx := context.Background()

	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatal(err)
	}
	before := graphChecksum(t, m, repo)

	writeFile(t, repo, "broken.go", "package main\nfunc Bad() { undefined(")

	// First query launches the doomed build and serves the old graph.
	resp, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "Announce"})
	if err != nil {
		t.Fatalf("degraded serve failed: %v", err)
	}
	if !resp.Freshness.Stale {
		t.Errorf("want stale on a broken repo, got %+v", resp.Freshness)
	}
	waitForBuild(t, m, repo, 30*time.Second)

	// The failed build must not have published anything.
	if after := graphChecksum(t, m, repo); after != before {
		t.Error("a failed build modified graph.db")
	}

	// The reason must now be visible, and the old graph must still answer.
	resp, err = m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "Announce"})
	if err != nil {
		t.Fatalf("degraded serve failed after the build error: %v", err)
	}
	if resp.Freshness.IndexError == "" {
		t.Error("IndexError should surface the type-check failure")
	}
	if !strings.Contains(resp.Freshness.IndexError, "type-check") {
		t.Errorf("unexpected IndexError: %q", resp.Freshness.IndexError)
	}
	if resp.Symbol == nil {
		t.Error("old graph should still resolve symbols while broken")
	}
}

func TestBuildAsync_SuccessClearsError(t *testing.T) {
	repo := copyFixture(t)
	m := managerFor(t)
	ctx := context.Background()

	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "broken.go", "package main\nfunc Bad() { undefined(")
	if _, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "Announce"}); err != nil {
		t.Fatal(err)
	}
	waitForBuild(t, m, repo, 30*time.Second)

	// Fix it — a valid file at the same path keeps the stamp moving forward.
	writeFile(t, repo, "broken.go", "package main\n\nfunc Recovered() {}\n")

	resp, err := m.WaitBuild(ctx, repo, 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Completed || !resp.Rebuilt {
		t.Fatalf("want a successful rebuild after the fix, got %+v", resp)
	}
	if resp.Freshness.IndexError != "" {
		t.Errorf("a successful build must clear IndexError, got %q", resp.Freshness.IndexError)
	}
	if _, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "Recovered"}); err != nil {
		t.Fatalf("recovered symbol missing: %v", err)
	}
}

// --- D4: a repo that fails to build must not relaunch on every query ---

// TestEnsureFresh_NoRebuildLoopOnPersistentFailure pins D4: a broken repo used
// to launch a full go/packages load on *every* query — the state where an agent
// queries most. A stamp that already failed must serve stale without rebuilding.
func TestEnsureFresh_NoRebuildLoopOnPersistentFailure(t *testing.T) {
	repo := copyFixture(t)
	m := managerFor(t)
	ctx := context.Background()

	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "broken.go", "package main\nfunc Bad() { undefined(")

	if _, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "Announce"}); err != nil {
		t.Fatal(err)
	}
	waitForBuild(t, m, repo, 30*time.Second)

	before := buildNonce.Load()
	for range 5 {
		resp, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "Announce"})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Freshness.IndexError == "" {
			t.Error("a known-broken stamp must keep reporting why it is stale")
		}
		waitForBuild(t, m, repo, 30*time.Second)
	}
	if got := buildNonce.Load() - before; got != 0 {
		t.Errorf("%d rebuild(s) launched for an already-failed stamp, want 0", got)
	}

	// A new stamp must break the suppression — this is not a permanent latch.
	writeFile(t, repo, "broken.go", "package main\n\nfunc Recovered() {}\n")
	resp, err := m.WaitBuild(ctx, repo, 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Completed || !resp.Rebuilt {
		t.Fatalf("a moved stamp must retry the build, got %+v", resp)
	}
}

// graphChecksum returns the size+mtime of the repo's graph.db as a cheap
// "did anything publish" probe.
func graphChecksum(t *testing.T, m *Manager, repo string) string {
	t.Helper()
	canon, err := canonicalRepo(repo)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(m.dbPath(canon))
	if err != nil {
		t.Fatal(err)
	}
	return info.ModTime().String() + ":" + itoa(info.Size())
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestEnsureFresh_ColdBuildSurvivesCallerCancel pins D6: a cold build used to
// run on the caller's context, so a client that disconnected mid-index killed
// it and discarded every second of work — the next query started from zero.
// The build must finish and publish regardless of the caller going away.
func TestEnsureFresh_ColdBuildSurvivesCallerCancel(t *testing.T) {
	repo := copyFixture(t)
	m := managerFor(t)
	canon, err := canonicalRepo(repo)
	if err != nil {
		t.Fatal(err)
	}

	// Caller is already gone before the build starts — the harshest version of
	// a disconnect, and deterministic.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// The caller is told its own context died. That part is correct.
	_, release, _, err := m.ensureFresh(ctx, repo)
	release()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled caller should get its own ctx error, got %v", err)
	}

	// ...but the build must survive it. The next query blocks on the repo lock
	// the abandoned build still holds, then finds a finished graph — it must not
	// start over, and it must not come back stale.
	resp, err := m.Symbol(context.Background(), SymbolRequest{Repo: repo, Symbol: "Announce"})
	if err != nil {
		t.Fatalf("abandoned cold build discarded its work: %v", err)
	}
	if resp.Freshness.Stale {
		t.Errorf("expected a fresh graph from the surviving build, got %+v", resp.Freshness)
	}
	if _, err := os.Stat(m.dbPath(canon)); err != nil {
		t.Fatalf("no graph published: %v", err)
	}
}

// TestWaitBuild_BadRepoPath keeps the error path honest: a nonexistent repo is
// an error, not a Completed:false response.
func TestWaitBuild_BadRepoPath(t *testing.T) {
	m := managerFor(t)
	_, err := m.WaitBuild(context.Background(), filepath.Join(t.TempDir(), "nope"), time.Second)
	if err == nil {
		t.Fatal("want an error for a nonexistent repo")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wrong error: %v", err)
	}
}
