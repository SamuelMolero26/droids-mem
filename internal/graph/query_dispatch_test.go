package graph

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestCallersViaInterface_Split pins tasks 6.4/6.5 (spec "Dispatch is
// labelled per edge and split at response level"): Dominant.Do has 3
// interface callers + 1 static; Minor.Do shares the same 3 interface call
// sites but has 4 static callers too. The interface count is the same 3 in
// both cases — it is a count of edges, not a share of the caller list.
func TestCallersViaInterface_Split(t *testing.T) {
	m, repo := testManagerAt(t, "testdata/dispatch")
	ctx := context.Background()

	resp, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "dispatch.Dominant.Do", Direction: "up"})
	if err != nil {
		t.Fatalf("Symbol Dominant.Do: %v", err)
	}
	if len(resp.Callers) != 4 {
		t.Fatalf("Dominant.Do callers = %d, want 4 (3 interface + 1 static): %+v", len(resp.Callers), resp.Callers)
	}
	if resp.CallersViaInterface != 3 {
		t.Errorf("Dominant.Do CallersViaInterface = %d, want 3", resp.CallersViaInterface)
	}

	resp, err = m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "dispatch.Minor.Do", Direction: "up"})
	if err != nil {
		t.Fatalf("Symbol Minor.Do: %v", err)
	}
	if len(resp.Callers) != 7 {
		t.Fatalf("Minor.Do callers = %d, want 7 (3 interface + 4 static): %+v", len(resp.Callers), resp.Callers)
	}
	if resp.CallersViaInterface != 3 {
		t.Errorf("Minor.Do CallersViaInterface = %d, want 3", resp.CallersViaInterface)
	}
	if strings.Contains(resp.Hint, "interface-dispatch candidates") {
		t.Errorf("Minor.Do (3 of 7 interface) must not carry the CHA hint: %q", resp.Hint)
	}
}

// TestSymbol_NoSource: the flag drops only the queried symbol's body; the
// signature, callers and counts are identical to the default response.
func TestSymbol_NoSource(t *testing.T) {
	m, repo := testManagerAt(t, "testdata/dispatch")
	ctx := context.Background()
	req := SymbolRequest{Repo: repo, Symbol: "dispatch.Dominant.Do", Direction: "up"}

	full, err := m.Symbol(ctx, req)
	if err != nil {
		t.Fatalf("Symbol: %v", err)
	}
	if full.Symbol.Source == "" {
		t.Fatal("precondition: default response must carry source")
	}
	req.NoSource = true
	got, err := m.Symbol(ctx, req)
	if err != nil {
		t.Fatalf("Symbol NoSource: %v", err)
	}
	if got.Symbol.Source != "" {
		t.Errorf("NoSource kept source: %q", got.Symbol.Source)
	}
	if strings.Contains(RenderSymbol(got), "source:") {
		t.Error("NoSource render still has a source block")
	}
	if got.Symbol.Signature != full.Symbol.Signature || !reflect.DeepEqual(got.Callers, full.Callers) ||
		got.CallersViaInterface != full.CallersViaInterface {
		t.Error("NoSource changed signature/callers/split; only source may differ")
	}
}

// TestDominantInterfaceCallers_Hint: 3 of 4 callers are interface dispatch,
// so the hint names the action (verify), not just the count.
func TestDominantInterfaceCallers_Hint(t *testing.T) {
	m, repo := testManagerAt(t, "testdata/dispatch")
	resp, err := m.Symbol(context.Background(), SymbolRequest{Repo: repo, Symbol: "dispatch.Dominant.Do", Direction: "up"})
	if err != nil {
		t.Fatalf("Symbol Dominant.Do: %v", err)
	}
	if want := "3 of 4 callers are interface-dispatch candidates"; !strings.Contains(resp.Hint, want) {
		t.Errorf("hint = %q, want it to contain %q", resp.Hint, want)
	}
}

// TestCapInvariance_SplitsAndTotalsIndependentOfCap pins task 7.1/7.2 (spec
// "Neighbor cap remains 50, a single tunable, no value-dependent branching"):
// shrinking maxNeighbors must change only the LENGTH of the returned
// Callers slice — ordering already covered elsewhere, and here: Truncated,
// CallersTotal, and every caller-fidelity split (CallersInTests,
// CallersViaInterface) must be identical regardless of the cap value,
// computed straight from the edges table, never from the capped slice.
func TestCapInvariance_SplitsAndTotalsIndependentOfCap(t *testing.T) {
	m, repo := testManagerAt(t, "testdata/dispatch")
	ctx := context.Background()

	query := func(cap int) *SymbolResponse {
		t.Helper()
		defer func(n int) { maxNeighbors = n }(maxNeighbors)
		maxNeighbors = cap
		resp, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "dispatch.Minor.Do", Direction: "up"})
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	small := query(2) // Minor.Do has 7 callers total; 2 forces truncation
	large := query(7) // exactly the total: no truncation

	if len(small.Callers) != 2 {
		t.Fatalf("small-cap Callers = %d, want 2 (only the shown slice length depends on the cap)", len(small.Callers))
	}
	if len(large.Callers) != 7 {
		t.Fatalf("large-cap Callers = %d, want 7", len(large.Callers))
	}
	if !small.Truncated || large.Truncated {
		t.Errorf("Truncated: small=%v (want true), large=%v (want false)", small.Truncated, large.Truncated)
	}
	if small.CallersTotal != 7 {
		t.Errorf("small-cap CallersTotal = %d, want the true total 7, independent of the cap", small.CallersTotal)
	}
	if small.CallersInTests != large.CallersInTests ||
		small.CallersViaInterface != large.CallersViaInterface {
		t.Errorf("split counts diverged with the cap: small={in_tests:%d via_iface:%d} large={in_tests:%d via_iface:%d}",
			small.CallersInTests, small.CallersViaInterface,
			large.CallersInTests, large.CallersViaInterface)
	}
}

// TestNeighbor_NoPerRowTestOrDispatchField pins task 6.7 (spec "No per-row
// dispatch field on the wire" / "No per-row test/production column on the
// wire"): the Neighbor JSON shape must carry no per-row test-ness or dispatch
// field — those exist only as response-level scalars (CallersInTests,
// CallersViaInterface). loc (File/Line) is the only per-row
// signal a caller of this API can derive test-ness from.
func TestNeighbor_NoPerRowTestOrDispatchField(t *testing.T) {
	typ := reflect.TypeOf(Neighbor{})
	for i := 0; i < typ.NumField(); i++ {
		tag := strings.ToLower(typ.Field(i).Tag.Get("json"))
		if strings.Contains(tag, "test") || strings.Contains(tag, "dispatch") || strings.Contains(tag, "origin") {
			t.Errorf("Neighbor field %q (json tag %q) is a per-row test/dispatch/origin field — spec requires response-level splits only",
				typ.Field(i).Name, tag)
		}
	}

	// Round-trip through the real encoder too, so a field added without a
	// struct-literal change (e.g. via an embedded type) is still caught.
	m, repo := testManagerAt(t, "testdata/dispatch")
	resp, err := m.Symbol(context.Background(), SymbolRequest{Repo: repo, Symbol: "dispatch.Dominant.Do", Direction: "up"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(resp.Callers)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(b, &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		for k := range row {
			lk := strings.ToLower(k)
			if strings.Contains(lk, "test") || strings.Contains(lk, "dispatch") {
				t.Errorf("caller row carries a per-row field %q, want response-level splits only: %v", k, row)
			}
		}
	}
}

// TestSymbol_NoTests: test-file callers leave the rows but stay in the
// callers_in_tests count; production callers are untouched.
func TestSymbol_NoTests(t *testing.T) {
	m, repo := testManagerAt(t, "testdata/pkgtest")
	ctx := context.Background()

	full, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "pkgtest.Real", Direction: "up"})
	if err != nil {
		t.Fatalf("Symbol: %v", err)
	}
	got, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "pkgtest.Real", Direction: "up", NoTests: true})
	if err != nil {
		t.Fatalf("Symbol NoTests: %v", err)
	}
	if len(full.Callers) != 2 || full.CallersInTests != 2 {
		t.Fatalf("precondition: Real callers = %d (in tests %d), want 2/2", len(full.Callers), full.CallersInTests)
	}
	if len(got.Callers) != 0 || got.CallersInTests != 2 {
		t.Errorf("NoTests callers = %d, in_tests = %d, want 0 rows and count 2", len(got.Callers), got.CallersInTests)
	}

	prod, err := m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "pkgtest.helper", Direction: "up", NoTests: true})
	if err != nil {
		t.Fatalf("Symbol helper: %v", err)
	}
	if len(prod.Callers) != 1 || prod.Callers[0].QName != "pkgtest.Real" {
		t.Errorf("production caller dropped by NoTests: %+v", prod.Callers)
	}
}
