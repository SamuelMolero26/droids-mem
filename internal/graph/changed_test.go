package graph

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// pinRebuild occupies repo's build slot with a never-finishing build for the
// CURRENT stamp, so ensureFresh deterministically attaches (Rebuilding:true)
// instead of racing a real rebuild that could land before the assertion. The
// returned func unpins early; cleanup unpins regardless.
func pinRebuild(t *testing.T, m *Manager, repo string) func() {
	t.Helper()
	canon, err := canonicalRepo(repo)
	if err != nil {
		t.Fatal(err)
	}
	current, err := stamp(canon)
	if err != nil {
		t.Fatal(err)
	}
	bctx, bcancel := context.WithCancel(context.Background())
	m.buildsMu.Lock()
	m.builds[canon] = &buildState{ctx: bctx, cancel: bcancel, stamp: current, done: make(chan struct{})}
	m.buildsMu.Unlock()
	unpin := func() {
		m.buildsMu.Lock()
		delete(m.builds, canon)
		m.buildsMu.Unlock()
		bcancel()
	}
	t.Cleanup(unpin)
	return unpin
}

// editZZ rewrites zz/zz.go with an extra function, keeping Hub and Near, so
// the edit is benign (still type-checks) and touches exactly one file.
func editZZ(t *testing.T, repo string) {
	t.Helper()
	writeFile(t, repo, "zz/zz.go", "package zz\n\n// Hub is the queried target.\nfunc Hub() {}\n\n// Near calls Hub from the same package.\nfunc Near() { Hub() }\n\n// Extra is new.\nfunc Extra() {}\n")
}

func neighborChanged(ns []Neighbor, qname string) (changed, found bool) {
	for _, n := range ns {
		if n.QName == qname {
			return n.Changed, true
		}
	}
	return false, false
}

// TestChanged_OnlyEditedFileIsFlagged pins the per-file freshness contract: an
// edit to one file must not flag answers drawn from untouched files as stale.
// The repo-wide bit used to mark every query STALE for the whole rebuild.
func TestChanged_OnlyEditedFileIsFlagged(t *testing.T) {
	repo := copyFixture(t)
	m := managerFor(t)
	ctx := context.Background()
	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatal(err)
	}
	editZZ(t, repo)
	pinRebuild(t, m, repo)

	// Untouched file: current answer, no flag, no rebuild hint.
	resp, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "Announce"})
	if err != nil {
		t.Fatal(err)
	}
	f := resp.Freshness
	if f.Stale || resp.Changed {
		t.Errorf("untouched symbol flagged: stale=%v changed=%v", f.Stale, resp.Changed)
	}
	if !f.Rebuilding {
		t.Error("want rebuilding while the pinned build is in flight")
	}
	if !slices.Equal(f.ChangedFiles, []string{"zz/zz.go"}) || f.ChangedFilesTotal != 1 {
		t.Errorf("changed files = %v (total %d), want [zz/zz.go]", f.ChangedFiles, f.ChangedFilesTotal)
	}
	if strings.Contains(resp.Hint, rebuildingHint) {
		t.Errorf("rebuild hint on a response with no changed row: %q", resp.Hint)
	}

	// Edited file: the symbol and its same-file neighbor are flagged.
	resp, err = m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "Hub"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Changed || resp.Freshness.Stale || !resp.Freshness.Rebuilding {
		t.Errorf("edited symbol: changed=%v stale=%v rebuilding=%v", resp.Changed, resp.Freshness.Stale, resp.Freshness.Rebuilding)
	}
	if !strings.Contains(resp.Hint, "graph_build_wait") {
		t.Errorf("changed answer during a rebuild lacks the rebuild hint: %q", resp.Hint)
	}
	if c, ok := neighborChanged(resp.Callers, "zz.Near"); !ok || !c {
		t.Errorf("same-file caller Near: found=%v changed=%v", ok, c)
	}
	if c, ok := neighborChanged(resp.Callers, "testmod.main"); !ok || c {
		t.Errorf("caller main (untouched main.go): found=%v changed=%v", ok, c)
	}

	// A neighbor living in the edited file is flagged from an untouched anchor.
	resp, err = m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "testmod.main", Direction: "down"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Changed {
		t.Error("main.go is untouched")
	}
	if c, ok := neighborChanged(resp.Callees, "zz.Hub"); !ok || !c {
		t.Errorf("callee zz.Hub: found=%v changed=%v", ok, c)
	}

	pkg, err := m.Package(ctx, PackageRequest{Repo: repo, Package: "zz"})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range pkg.Symbols {
		if !s.Changed {
			t.Errorf("package row %s in edited file not flagged", s.QName)
		}
	}
	if !strings.Contains(pkg.Hint, rebuildingHint) {
		t.Errorf("package with changed rows lacks the rebuild hint: %q", pkg.Hint)
	}
}

// TestChanged_AddedAndDeletedFilesListed: both an added and a deleted file
// differ from the indexed census, so both must be named.
func TestChanged_AddedAndDeletedFilesListed(t *testing.T) {
	repo := copyFixture(t)
	m := managerFor(t)
	ctx := context.Background()
	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "added.go", "package main\n\nfunc AddedSymbol() {}\n")
	if err := os.Remove(filepath.Join(repo, "zz", "zz.go")); err != nil {
		t.Fatal(err)
	}
	pinRebuild(t, m, repo)

	_, release, fresh, err := m.ensureFresh(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if want := []string{"added.go", "zz/zz.go"}; !slices.Equal(fresh.ChangedFiles, want) || fresh.ChangedFilesTotal != 2 {
		t.Errorf("changed files = %v (total %d), want %v", fresh.ChangedFiles, fresh.ChangedFilesTotal, want)
	}
	if fresh.Stale {
		t.Error("a pending rebuild is not a build failure")
	}
}

// TestChanged_ListIsCapped: the inline list is bounded like stale_units, while
// the total stays exact.
func TestChanged_ListIsCapped(t *testing.T) {
	repo := copyFixture(t)
	m := managerFor(t)
	ctx := context.Background()
	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatal(err)
	}
	for i := range 7 {
		writeFile(t, repo, fmt.Sprintf("extra%d.go", i), fmt.Sprintf("package main\n\nfunc Extra%d() {}\n", i))
	}
	pinRebuild(t, m, repo)

	_, release, fresh, err := m.ensureFresh(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	release()
	want := []string{"extra0.go", "extra1.go", "extra2.go", "extra3.go", "extra4.go"}
	if !slices.Equal(fresh.ChangedFiles, want) || fresh.ChangedFilesTotal != 7 {
		t.Errorf("changed files = %v (total %d), want %v (total 7)", fresh.ChangedFiles, fresh.ChangedFilesTotal, want)
	}
}

// TestChanged_BuildFailureIsStale: stale is reserved for the current source
// failing to build — the one case where the served index is old because it
// cannot be replaced.
func TestChanged_BuildFailureIsStale(t *testing.T) {
	repo := copyFixture(t)
	m := managerFor(t)
	ctx := context.Background()
	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatal(err)
	}
	breakMajority(t, repo)
	if _, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "Announce"}); err != nil {
		t.Fatal(err)
	}
	waitForBuild(t, m, repo, 30*time.Second)

	resp, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "Announce"})
	if err != nil {
		t.Fatal(err)
	}
	f := resp.Freshness
	if !f.Stale || f.IndexError == "" || f.Rebuilding {
		t.Errorf("failed build: want stale+index_error, no rebuild; got %+v", f)
	}
	if !slices.Contains(f.ChangedFiles, "broken.go") {
		t.Errorf("changed files %v lack the breaking file", f.ChangedFiles)
	}
}

// TestChanged_WaitBuildSettlesClean: after waiting out the rebuild, the graph
// matches the tree, so nothing is reported changed.
func TestChanged_WaitBuildSettlesClean(t *testing.T) {
	repo := copyFixture(t)
	m := managerFor(t)
	ctx := context.Background()
	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatal(err)
	}
	editZZ(t, repo)

	resp, err := m.WaitBuild(ctx, repo, 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Completed || !resp.Rebuilt {
		t.Fatalf("want completed+rebuilt, got %+v", resp)
	}
	if f := resp.Freshness; f.Stale || f.Rebuilding || f.ChangedFilesTotal != 0 || len(f.ChangedFiles) != 0 {
		t.Errorf("settled graph still reports changes: %+v", f)
	}
}

// TestChanged_FileStampsPersisted: the build stores the per-file census the
// diff is computed against.
func TestChanged_FileStampsPersisted(t *testing.T) {
	repo := copyFixture(t)
	m := managerFor(t)
	ctx := context.Background()
	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatal(err)
	}
	conn, release, _, err := m.ensureFresh(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	rows, err := conn.QueryContext(ctx, `SELECT path FROM file_stamps ORDER BY path`)
	if err != nil {
		t.Fatalf("file_stamps unreadable: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		got = append(got, p)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"go.mod", "main.go", "zz/zz.go"}; !slices.Equal(got, want) {
		t.Errorf("file_stamps = %v, want %v", got, want)
	}
}

func TestRender_ChangedFilesAndRows(t *testing.T) {
	r := &SymbolResponse{
		Repo:      "/repo",
		Freshness: Freshness{Stamp: "v1", Rebuilding: true, ChangedFiles: []string{"a.go"}, ChangedFilesTotal: 3},
		Symbol:    &SymbolInfo{QName: "p.F", Kind: "func", File: "a.go", Line: 1, Signature: "func F()"},
		Changed:   true,
		Callers:   []Neighbor{{QName: "p.G", Signature: "func G()", File: "a.go", Line: 4, Depth: 1, Changed: true}},
	}
	out := RenderSymbol(r)
	for _, want := range []string{"changed_files[1 of 3]: a.go", "REBUILDING", "changed: true", "a.go:4 (changed)"} {
		if !strings.Contains(out, want) {
			t.Errorf("render lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "STALE") {
		t.Errorf("a pending rebuild rendered as STALE:\n%s", out)
	}

	p := RenderPackage(&PackageResponse{
		Repo:    "/repo",
		Package: "p",
		Symbols: []PackageSymbol{{QName: "p.F", Kind: "func", Signature: "func F()", File: "a.go", Line: 1, Changed: true}},
	})
	if !strings.Contains(p, "a.go:1 (changed)") {
		t.Errorf("package render lacks the changed marker:\n%s", p)
	}
}

// TestChanged_AddedCallerInOtherFileIsDisclosed: a caller added in a changed
// file is invisible until the rebuild lands, and the queried symbol's own file
// is untouched, so no row carries the flag. The response must still say its
// caller list may be incomplete — and stop saying so once the graph settles.
func TestChanged_AddedCallerInOtherFileIsDisclosed(t *testing.T) {
	repo := copyFixture(t)
	m := managerFor(t)
	ctx := context.Background()
	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "zz/extra.go", "package zz\n\n// Extra calls Hub.\nfunc Extra() { Hub() }\n")
	unpin := pinRebuild(t, m, repo)

	up, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "Hub", Direction: "up"})
	if err != nil {
		t.Fatal(err)
	}
	if up.Changed {
		t.Fatal("zz.go is untouched")
	}
	if !strings.Contains(up.Hint, "1 file(s) changed since indexing") || !strings.Contains(up.Hint, "may be missing") {
		t.Errorf("caller list from an outdated graph lacks the additions hint: %q", up.Hint)
	}

	// Callee-only answers and package surfaces are not caller lists.
	down, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "Hub", Direction: "down"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(down.Hint, "may be missing") {
		t.Errorf("callee-only answer got the additions hint: %q", down.Hint)
	}
	pkg, err := m.Package(ctx, PackageRequest{Repo: repo, Package: "zz"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(pkg.Hint, "may be missing") {
		t.Errorf("package answer got the additions hint: %q", pkg.Hint)
	}

	unpin()
	if w, err := m.WaitBuild(ctx, repo, 60*time.Second); err != nil || !w.Rebuilt {
		t.Fatalf("wait: %+v %v", w, err)
	}
	up, err = m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "Hub", Direction: "up"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(up.Hint, "may be missing") {
		t.Errorf("settled graph still warns of missing callers: %q", up.Hint)
	}
	if _, ok := neighborChanged(up.Callers, "zz.Extra"); !ok {
		t.Errorf("rebuilt graph lacks the added caller: %+v", up.Callers)
	}
}
