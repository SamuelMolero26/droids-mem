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

func testManager(t *testing.T) (*Manager, string) {
	return testManagerAt(t, "testdata/testmod")
}

func testManagerAt(t *testing.T, rel string) (*Manager, string) {
	t.Helper()
	m := NewManager(filepath.Join(t.TempDir(), "graphs"))
	t.Cleanup(m.Close)
	repo, err := filepath.Abs(rel)
	if err != nil {
		t.Fatal(err)
	}
	return m, repo
}

func TestIndexAndSymbol(t *testing.T) {
	m, repo := testManager(t)
	ctx := context.Background()

	idx, err := m.Index(ctx, repo)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if idx.Symbols == 0 || idx.Edges == 0 {
		t.Fatalf("empty index: %+v", idx)
	}

	resp, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "Announce"})
	if err != nil {
		t.Fatalf("Symbol: %v", err)
	}
	if resp.Symbol == nil || resp.Symbol.QName != "testmod.Announce" {
		t.Fatalf("wrong symbol: %+v", resp.Symbol)
	}
	if !strings.Contains(resp.Symbol.Source, "pick().Greet()") {
		t.Errorf("source body missing: %q", resp.Symbol.Source)
	}
	wantCallee := func(name string) {
		for _, n := range resp.Callees {
			if n.QName == name {
				return
			}
		}
		t.Errorf("callee %s missing from %+v", name, resp.Callees)
	}
	wantCallee("testmod.pick")
	wantCallee("testmod.English.Greet") // interface dispatch resolved by CHA
	for _, n := range resp.Callers {
		if n.QName == "testmod.main" {
			return
		}
	}
	t.Errorf("caller testmod.main missing from %+v", resp.Callers)
}

func TestSymbolPath(t *testing.T) {
	m, repo := testManager(t)
	resp, err := m.Symbol(context.Background(), SymbolRequest{
		Repo: repo, Symbol: "testmod.main", To: "English.Greet",
	})
	if err != nil {
		t.Fatalf("Symbol path: %v", err)
	}
	var got []string
	for _, n := range resp.Path {
		got = append(got, n.QName)
	}
	want := "testmod.main → testmod.Announce → testmod.English.Greet"
	if strings.Join(got, " → ") != want {
		t.Errorf("path = %v, want %s", got, want)
	}
}

func TestPackageSurface(t *testing.T) {
	m, repo := testManager(t)
	resp, err := m.Package(context.Background(), PackageRequest{Repo: repo, Package: "testmod"})
	if err != nil {
		t.Fatalf("Package: %v", err)
	}
	names := map[string]bool{}
	for _, s := range resp.Symbols {
		names[s.QName] = true
	}
	for _, want := range []string{"testmod.Greeter", "testmod.English", "testmod.Announce", "testmod.English.Greet"} {
		if !names[want] {
			t.Errorf("exported symbol %s missing from %v", want, names)
		}
	}
	if names["testmod.pick"] || names["testmod.main"] {
		t.Errorf("unexported symbols leaked into surface: %v", names)
	}
	if resp.Unexported == 0 {
		t.Error("unexported_count should be > 0")
	}
}

func TestStalenessRebuildAndDegradedServe(t *testing.T) {
	m, repo := testManager(t)
	ctx := context.Background()

	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatal(err)
	}

	// Break the repo: staleness check must trip, rebuild must fail, and the
	// last good graph must be served with Stale set.
	broken := filepath.Join(repo, "broken_fixture.go")
	if err := os.WriteFile(broken, []byte("package main\nfunc Bad() { undefined("), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(broken) })
	future := time.Now().Add(2 * time.Second) // ensure the stamp moves
	_ = os.Chtimes(broken, future, future)

	resp, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "Announce"})
	if err != nil {
		t.Fatalf("Symbol on broken repo: %v", err)
	}
	if !resp.Freshness.Stale || resp.Freshness.IndexError == "" {
		t.Errorf("expected stale degraded serve, got %+v", resp.Freshness)
	}

	// Fix the repo: next query must rebuild and clear the stale flag.
	if err := os.Remove(broken); err != nil {
		t.Fatal(err)
	}
	resp, err = m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "Announce"})
	if err != nil {
		t.Fatalf("Symbol after fix: %v", err)
	}
	if resp.Freshness.Stale {
		t.Errorf("still stale after repo fixed: %+v", resp.Freshness)
	}
}

func TestSymbolNotFound(t *testing.T) {
	m, repo := testManager(t)
	_, err := m.Symbol(context.Background(), SymbolRequest{Repo: repo, Symbol: "NoSuchThing"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// transitive_callers reports blast size on an exact match, and is absent on a
// search-menu response (no single symbol to score).
func TestTransitiveCallers(t *testing.T) {
	m, repo := testManager(t)
	ctx := context.Background()

	// main → Announce, so Announce's up-closure is {main} = 1; main has none.
	resp, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "Announce"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.TransitiveCallers == nil || *resp.TransitiveCallers != 1 {
		t.Errorf("Announce transitive_callers = %v, want 1", resp.TransitiveCallers)
	}
	resp, err = m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.TransitiveCallers == nil || *resp.TransitiveCallers != 0 {
		t.Errorf("main transitive_callers = %v, want 0", resp.TransitiveCallers)
	}
}

// transitive_callers is a call-edge metric, so it is omitted for non-callable
// symbols (types/consts/vars); the hint redirects by kind instead of reporting a
// structural 0 an agent would misread as "safe to change" (issue #47).
func TestBlastRadiusSuppressed(t *testing.T) {
	m, repo := testManager(t)
	ctx := context.Background()

	cases := []struct {
		symbol string
		hint   string
	}{
		{"English", blastTypeHint}, // a type: redirect to its methods
		{"Lang", blastRefHint},     // a const: reference-level, not indexed
	}
	for _, tc := range cases {
		resp, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: tc.symbol})
		if err != nil {
			t.Fatalf("Symbol %s: %v", tc.symbol, err)
		}
		if resp.TransitiveCallers != nil {
			t.Errorf("%s: transitive_callers = %v, want omitted (nil)", tc.symbol, *resp.TransitiveCallers)
		}
		if resp.Hint != tc.hint {
			t.Errorf("%s: hint = %q, want %q", tc.symbol, resp.Hint, tc.hint)
		}
	}
}

// A name that doesn't resolve falls back to a BM25 search menu instead of
// not-found, with no scored symbol attached.
func TestSearchFallback(t *testing.T) {
	m, repo := testManager(t)
	resp, err := m.Symbol(context.Background(), SymbolRequest{Repo: repo, Symbol: "greeting through interface"})
	if err != nil {
		t.Fatalf("search fallback errored: %v", err)
	}
	if resp.Symbol != nil {
		t.Errorf("search response should carry no exact symbol, got %+v", resp.Symbol)
	}
	if len(resp.Matches) == 0 {
		t.Fatal("search fallback returned no matches")
	}
	if resp.TransitiveCallers != nil {
		t.Errorf("search menu should not score a symbol, got %v", *resp.TransitiveCallers)
	}
	if resp.Hint != searchHint {
		t.Errorf("hint = %q, want searchHint", resp.Hint)
	}
}

// TestInterfaceFanOut measures CHA over-approximation on a multi-implementation
// interface: two call sites dispatching through Store, three implementations.
// CHA links each call site to every implementation, including MockStore, which
// is never constructed, so fan-out = call_sites × implementations = 6, and
// every MockStore.Save edge is unreachable at runtime.
func TestInterfaceFanOut(t *testing.T) {
	m, repo := testManagerAt(t, "testdata/fanout")
	ctx := context.Background()

	want := map[string]bool{
		"fanout.SQLStore.Save":  true,
		"fanout.MemStore.Save":  true,
		"fanout.MockStore.Save": true,
	}
	calleeSet := func(sym string) map[string]bool {
		resp, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: sym, Direction: "down"})
		if err != nil {
			t.Fatalf("Symbol %s: %v", sym, err)
		}
		got := map[string]bool{}
		for _, n := range resp.Callees {
			got[n.QName] = true
		}
		return got
	}

	fanout := 0
	for _, site := range []string{"Broadcast", "Echo"} {
		got := calleeSet(site)
		for qn := range want {
			if !got[qn] {
				t.Errorf("%s missing callee %s; got %v", site, qn, got)
			}
		}
		if len(got) != len(want) {
			t.Errorf("%s callees = %v, want exactly %v", site, got, want)
		}
		fanout += len(got)
	}
	if fanout != 6 {
		t.Errorf("interface-dispatch fan-out = %d, want 6 (2 sites × 3 impls)", fanout)
	}

	// MockStore is never constructed, yet its blast radius equals the constructed
	// implementations': {Broadcast, Echo, Run} transitively reach MockStore.Save.
	resp, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "fanout.MockStore.Save"})
	if err != nil {
		t.Fatalf("Symbol MockStore.Save: %v", err)
	}
	if resp.TransitiveCallers == nil || *resp.TransitiveCallers != 3 {
		t.Errorf("MockStore.Save transitive_callers = %v, want 3", resp.TransitiveCallers)
	}
}

// A _test.go edit must NOT move the stamp (test files are never indexed, so a
// rebuild would be pure waste); a source .go edit must.
func TestStampIgnoresTestFiles(t *testing.T) {
	repo := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module x\n\ngo 1.21\n")
	write("a.go", "package x\n\nfunc A() {}\n")
	write("a_test.go", "package x\n")

	base, err := stamp(repo)
	if err != nil {
		t.Fatal(err)
	}

	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(filepath.Join(repo, "a_test.go"), future, future)
	if s, _ := stamp(repo); s != base {
		t.Errorf("test-file edit moved stamp: %q → %q", base, s)
	}

	_ = os.Chtimes(filepath.Join(repo, "a.go"), future, future)
	if s, _ := stamp(repo); s == base {
		t.Errorf("source edit did not move stamp: still %q", base)
	}
}

// TestRemoveStaleTemps proves the crash-litter sweep is age-guarded: an orphan
// older than staleTempAge is removed, a young temp (a concurrently-live sibling
// build) is left untouched.
func TestRemoveStaleTemps(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "graph.db")
	old := dbPath + ".tmp.111"
	young := dbPath + ".tmp.222"
	for _, p := range []string{old, young} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-2 * staleTempAge)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}

	removeStaleTemps(dbPath)

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("stale temp survived the sweep: err=%v", err)
	}
	if _, err := os.Stat(young); err != nil {
		t.Errorf("young temp (live sibling) was removed: %v", err)
	}
}
