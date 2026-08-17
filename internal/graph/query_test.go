package graph

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestNeighborOrdering_ProductionBeforeTestUnderCap pins the ordering hazard
// Tests:true introduces (issue: same-package test callers used to sort ahead
// of cross-package production callers under the pre-existing
// `(s.package != ?)`-only ORDER BY, so at a fixed cap an agent could see ZERO
// production callers and wrongly conclude a signature change is test-only).
// zz.Hub has 2 production callers (zz.Near, same-package; testmod.main,
// cross-package) and 3 same-package test callers added below — 5 total, cap
// smaller than that. Every shown caller under the cap must be a production
// caller.
func TestNeighborOrdering_ProductionBeforeTestUnderCap(t *testing.T) {
	repo := copyFixture(t)
	writeFile(t, filepath.Join(repo, "zz"), "hub_test.go", `package zz

import "testing"

func TestHubA(t *testing.T) { Hub() }
func TestHubB(t *testing.T) { Hub() }
func TestHubC(t *testing.T) { Hub() }
`)

	defer func(n int) { maxNeighbors = n }(maxNeighbors)
	maxNeighbors = 2 // smaller than the 5 total callers (2 production + 3 test)

	m := NewManager(filepath.Join(t.TempDir(), "graphs"))
	t.Cleanup(m.Close)
	resp, err := m.Symbol(context.Background(), SymbolRequest{Repo: repo, Symbol: "zz.Hub", Direction: "up"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Callers) != 2 {
		t.Fatalf("want 2 callers at the cap, got %+v", resp.Callers)
	}
	for _, c := range resp.Callers {
		if strings.HasSuffix(c.File, "_test.go") {
			t.Errorf("test caller %q shown under the cap while a production caller exists: %+v", c.QName, resp.Callers)
		}
	}
	if resp.Callers[0].QName != "zz.Near" {
		t.Errorf("within the production group, same-package proximity must be preserved: got %q first, want zz.Near", resp.Callers[0].QName)
	}
}

// TestNeighborOrdering_ProximityPreservedWithinTestGroup checks the second
// half of the ordering contract: same-package proximity still applies WITHIN
// the test-caller group, not just the production one. zz.Hub's test callers
// here are all same-package (zz), so this also confirms the test group is not
// dropped once every production caller has been shown.
func TestNeighborOrdering_ProximityPreservedWithinTestGroup(t *testing.T) {
	repo := copyFixture(t)
	writeFile(t, filepath.Join(repo, "zz"), "hub_test.go", `package zz

import "testing"

func TestHubA(t *testing.T) { Hub() }
`)

	m := NewManager(filepath.Join(t.TempDir(), "graphs"))
	t.Cleanup(m.Close)
	resp, err := m.Symbol(context.Background(), SymbolRequest{Repo: repo, Symbol: "zz.Hub", Direction: "up"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Callers) != 3 {
		t.Fatalf("want 3 callers (2 production + 1 test), got %+v", resp.Callers)
	}
	var sawTest bool
	for i, c := range resp.Callers {
		isTest := strings.HasSuffix(c.File, "_test.go")
		if isTest {
			sawTest = true
			continue
		}
		if sawTest {
			t.Errorf("production caller %q (index %d) sorted after a test caller", c.QName, i)
		}
	}
	if !sawTest {
		t.Fatal("test caller TestHubA missing from callers")
	}
}
