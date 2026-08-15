package graph

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// seedRawGraphMeta writes a minimal real graph.db at path carrying only the
// meta rows under test — a hand-seeded fixture standing in for "a graph.db
// whose most recent build recorded these carried units", open()'s only input
// for the fields under test here. Reuses the schema const, same pattern as
// carry_test.go's seedPrevGraph.
func seedRawGraphMeta(t *testing.T, path, stampVal string, carriedUnits []string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	meta := map[string]string{
		"stamp":         stampVal,
		"indexed_at":    "2026-01-01T00:00:00Z",
		"carried_units": strings.Join(carriedUnits, "\n"),
	}
	for k, v := range meta {
		if _, err := db.Exec(`INSERT INTO meta (key, value) VALUES (?,?)`, k, v); err != nil {
			t.Fatal(err)
		}
	}
}

// TestOpen_CapsStaleUnitsAtFive pins task 5.3/5.4 (spec "Freshness reports
// carried units, capped"): 213 carried units must surface as the first 5
// names plus a total of 213, never inlined wholesale.
func TestOpen_CapsStaleUnitsAtFive(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	units := make([]string, 213)
	for i := range units {
		units[i] = fmt.Sprintf("pkg%03d", i)
	}
	seedRawGraphMeta(t, dbPath, "v1", units)

	m := NewManager(t.TempDir())
	t.Cleanup(m.Close)
	_, release, fresh, err := m.open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer release()

	if fresh.StaleUnitsTotal != 213 {
		t.Errorf("StaleUnitsTotal = %d, want 213", fresh.StaleUnitsTotal)
	}
	if len(fresh.StaleUnits) != 5 {
		t.Fatalf("StaleUnits has %d entries, want 5 (capped), got %v", len(fresh.StaleUnits), fresh.StaleUnits)
	}
	if !slices.Equal(fresh.StaleUnits, units[:5]) {
		t.Errorf("StaleUnits = %v, want the first 5 of %v", fresh.StaleUnits, units)
	}
}

// TestOpen_NoCarriedUnitsIsZeroValue guards the common case: a graph built
// with no broken packages must report a zero StaleUnitsTotal and an empty
// StaleUnits, not an off-by-one from an empty-string split.
func TestOpen_NoCarriedUnitsIsZeroValue(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "graph.db")
	seedRawGraphMeta(t, dbPath, "v1", nil)

	m := NewManager(t.TempDir())
	t.Cleanup(m.Close)
	_, release, fresh, err := m.open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer release()

	if fresh.StaleUnitsTotal != 0 {
		t.Errorf("StaleUnitsTotal = %d, want 0", fresh.StaleUnitsTotal)
	}
	if len(fresh.StaleUnits) != 0 {
		t.Errorf("StaleUnits = %v, want empty", fresh.StaleUnits)
	}
}

// TestPartialBuildSucceeds_NotStale pins spec "Partial build is not marked
// stale": a build that partitions packages (1-of-2 broken, well under the
// >50% cap), carries edges, and successfully writes a fresh graph.db must
// report Freshness.Stale == false — the build itself succeeded, even though
// some units carry forward edges from the previous graph.
func TestPartialBuildSucceeds_NotStale(t *testing.T) {
	repo := copyFixture(t)
	m := managerFor(t)
	ctx := context.Background()

	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatal(err)
	}
	// Break only zz (1 of testmod's 2 packages) — under the >50% cap, so the
	// build must partition and succeed rather than serve the previous graph.
	writeFile(t, filepath.Join(repo, "zz"), "zz_broken.go", `package zz

func Broken() {
	var x int = "this does not type-check"
	_ = x
}
`)
	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatalf("partial build (1-of-2 broken) must succeed, got: %v", err)
	}

	resp, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "Announce"})
	if err != nil {
		t.Fatalf("Symbol: %v", err)
	}
	if resp.Freshness.Stale {
		t.Errorf("a partial build that itself succeeded must not be marked stale, got %+v", resp.Freshness)
	}
	if resp.Freshness.StaleUnitsTotal != 1 {
		t.Errorf("StaleUnitsTotal = %d, want 1 (zz)", resp.Freshness.StaleUnitsTotal)
	}
}

// TestSymbol_CarriedFlag pins task 6.9/6.10 (spec "Per-symbol carried flag"):
// a symbol whose package rode on carried-forward edges in the most recent
// build must report Carried:true; a symbol in a cleanly-built package must
// report Carried:false.
func TestSymbol_CarriedFlag(t *testing.T) {
	repo := copyFixture(t)
	m := managerFor(t)
	ctx := context.Background()

	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatal(err)
	}
	// Break only zz (1 of 2 packages) — partial build, not majority.
	writeFile(t, filepath.Join(repo, "zz"), "zz_broken.go", `package zz

func Broken() {
	var x int = "this does not type-check"
	_ = x
}
`)
	if _, err := m.Index(ctx, repo); err != nil {
		t.Fatalf("partial build (1-of-2 broken) must succeed, got: %v", err)
	}

	resp, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "zz.Hub"})
	if err != nil {
		t.Fatalf("Symbol zz.Hub: %v", err)
	}
	if !resp.Carried {
		t.Errorf("zz.Hub is in the broken (carried) package zz, want Carried:true, got %+v", resp)
	}

	resp, err = m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "Announce"})
	if err != nil {
		t.Fatalf("Symbol Announce: %v", err)
	}
	if resp.Carried {
		t.Errorf("Announce is in the cleanly-built package testmod, want Carried:false, got %+v", resp)
	}
}
