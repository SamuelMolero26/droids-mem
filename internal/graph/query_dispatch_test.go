package graph

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestCallersViaInterface_SplitAndDominanceHint pins tasks 6.4/6.5/6.6 (spec
// "Dispatch is labelled per edge and split at response level" +
// "Dispatch-dominance hint"): Dominant.Do has 3 interface callers + 1 static
// (75%, over the 50% dispatchHintRatio — hint must fire); Minor.Do shares the
// same 3 interface call sites but has 6 static callers too (33%, under the
// ratio — hint must not fire).
func TestCallersViaInterface_SplitAndDominanceHint(t *testing.T) {
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
	if !strings.Contains(resp.Hint, dispatchDominanceHint) {
		t.Errorf("Dominant.Do (75%% interface) hint missing dominance warning, got %q", resp.Hint)
	}

	resp, err = m.Symbol(ctx, SymbolRequest{Repo: repo, Symbol: "dispatch.Minor.Do", Direction: "up"})
	if err != nil {
		t.Fatalf("Symbol Minor.Do: %v", err)
	}
	if len(resp.Callers) != 9 {
		t.Fatalf("Minor.Do callers = %d, want 9 (3 interface + 6 static): %+v", len(resp.Callers), resp.Callers)
	}
	if resp.CallersViaInterface != 3 {
		t.Errorf("Minor.Do CallersViaInterface = %d, want 3", resp.CallersViaInterface)
	}
	if strings.Contains(resp.Hint, dispatchDominanceHint) {
		t.Errorf("Minor.Do (33%% interface) must not fire the dominance hint, got %q", resp.Hint)
	}
}

// TestCapInvariance_SplitsAndTotalsIndependentOfCap pins task 7.1/7.2 (spec
// "Neighbor cap remains 50, a single tunable, no value-dependent branching"):
// shrinking maxNeighbors must change only the LENGTH of the returned
// Callers slice — ordering already covered elsewhere, and here: Truncated,
// CallersTotal, and every caller-fidelity split (CallersInTests,
// CallerTestFiles, CallersViaInterface) must be identical regardless of the
// cap value, computed straight from the edges table, never from the capped
// slice.
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

	small := query(2) // Minor.Do has 9 callers total; 2 forces truncation
	large := query(9) // exactly the total: no truncation

	if len(small.Callers) != 2 {
		t.Fatalf("small-cap Callers = %d, want 2 (only the shown slice length depends on the cap)", len(small.Callers))
	}
	if len(large.Callers) != 9 {
		t.Fatalf("large-cap Callers = %d, want 9", len(large.Callers))
	}
	if !small.Truncated || large.Truncated {
		t.Errorf("Truncated: small=%v (want true), large=%v (want false)", small.Truncated, large.Truncated)
	}
	if small.CallersTotal != 9 {
		t.Errorf("small-cap CallersTotal = %d, want the true total 9, independent of the cap", small.CallersTotal)
	}
	if small.CallersInTests != large.CallersInTests ||
		small.CallerTestFiles != large.CallerTestFiles ||
		small.CallersViaInterface != large.CallersViaInterface {
		t.Errorf("split counts diverged with the cap: small={in_tests:%d files:%d via_iface:%d} large={in_tests:%d files:%d via_iface:%d}",
			small.CallersInTests, small.CallerTestFiles, small.CallersViaInterface,
			large.CallersInTests, large.CallerTestFiles, large.CallersViaInterface)
	}
}

// TestNeighbor_NoPerRowTestOrDispatchField pins task 6.7 (spec "No per-row
// dispatch field on the wire" / "No per-row test/production column on the
// wire"): the Neighbor JSON shape must carry no per-row test-ness or dispatch
// field — those exist only as response-level scalars (CallersInTests,
// CallerTestFiles, CallersViaInterface). loc (File/Line) is the only per-row
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
