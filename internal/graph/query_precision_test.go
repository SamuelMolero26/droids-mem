// Precision-on-the-wire tests (PR-F, design D7): SymbolResponse.Precision,
// the weakest-wins derivation it stands in for, and syntacticHint's position
// in the existing hint chain (design D5/D7's ordered chain: base/early-return
// assign, blastHint assign, carriedHint append, rebuildingHint append,
// syntacticHint append, truncatedHint append later).
package graph

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestPrecisionField_ResolvedForGoSymbol pins F.1/F.2: a Go (.go) symbol's
// response carries Precision == "resolved", derived from its own file
// extension with zero extra SQL (design D7) — and carries no syntactic
// caveat.
func TestPrecisionField_ResolvedForGoSymbol(t *testing.T) {
	repo := copyFixture(t)
	m := NewManager(filepath.Join(t.TempDir(), "graphs"))
	t.Cleanup(m.Close)

	resp, err := m.Symbol(context.Background(), SymbolRequest{Repo: repo, Symbol: "zz.Hub"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Precision != precisionResolved {
		t.Errorf("Precision = %q, want %q", resp.Precision, precisionResolved)
	}
	if strings.Contains(resp.Hint, syntacticHint) {
		t.Errorf("hint unexpectedly carries syntacticHint for a resolved (Go) symbol: %q", resp.Hint)
	}
}

// TestPrecisionField_SyntacticForMapperSymbol pins F.1/F.2/F.6 for the mapper
// tier: a .ts symbol's response carries Precision == "syntactic" AND the
// syntacticHint caveat.
func TestPrecisionField_SyntacticForMapperSymbol(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "app.ts", "export function helper(): void {}\nexport function run(): void { helper(); }\n")

	m := managerFor(t)
	resp, err := m.Symbol(context.Background(), SymbolRequest{Repo: repo, Symbol: "helper"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Precision != precisionSyntactic {
		t.Errorf("Precision = %q, want %q", resp.Precision, precisionSyntactic)
	}
	if !strings.Contains(resp.Hint, syntacticHint) {
		t.Errorf("hint = %q, want it to contain syntacticHint", resp.Hint)
	}
}

// TestSyntacticHint_AppendsAfterBlastHintWithoutClobbering pins F.5: for a
// mapper-tier interface (query.go's "interface" case assigns resp.Hint =
// implementersHint, the exact blastHint-style clobber trap F.5 exists to
// avoid), syntacticHint must still be present afterward — appended, never
// lost — and must come AFTER implementersHint in the joined string, matching
// the ordered chain (blastHint assign, then carriedHint/rebuildingHint/
// syntacticHint appends).
func TestSyntacticHint_AppendsAfterBlastHintWithoutClobbering(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "app.ts", "export interface Foo {\n  bar(): void;\n}\n")

	m := managerFor(t)
	resp, err := m.Symbol(context.Background(), SymbolRequest{Repo: repo, Symbol: "Foo"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Symbol == nil || resp.Symbol.Kind != "interface" {
		t.Fatalf("test setup: want an interface symbol, got %+v", resp.Symbol)
	}
	if resp.Precision != precisionSyntactic {
		t.Fatalf("Precision = %q, want %q", resp.Precision, precisionSyntactic)
	}
	implIdx := strings.Index(resp.Hint, implementersHint)
	syntIdx := strings.Index(resp.Hint, syntacticHint)
	if implIdx < 0 {
		t.Fatalf("blastHint (implementersHint) missing from resp.Hint — clobbered: %q", resp.Hint)
	}
	if syntIdx < 0 {
		t.Fatalf("syntacticHint missing from resp.Hint: %q", resp.Hint)
	}
	if syntIdx < implIdx {
		t.Errorf("syntacticHint must come after the blastHint assignment in resp.Hint, got %q", resp.Hint)
	}
}

// TestMixedTransitiveCallers_WeakestPrecisionWins pins F.3/F.4: 3 resolved +
// 2 syntactic precisions (a hypothetical mixed transitive_callers answer —
// unreachable in production given tier disjointness, D.7) must resolve to
// "syntactic", the weaker claim.
func TestMixedTransitiveCallers_WeakestPrecisionWins(t *testing.T) {
	mixed := []string{"resolved", "resolved", "resolved", "syntactic", "syntactic"}
	if len(mixed) != 5 {
		t.Fatalf("test setup: want 5 precisions (count==5 per spec scenario), got %d", len(mixed))
	}
	if got := weakestPrecision(mixed); got != precisionSyntactic {
		t.Errorf("weakestPrecision(%v) = %q, want %q", mixed, got, precisionSyntactic)
	}
}

// TestMixedTransitiveCallers_AllResolved pins the companion scenario: an
// all-resolved precision set stays "resolved".
func TestMixedTransitiveCallers_AllResolved(t *testing.T) {
	allResolved := []string{"resolved", "resolved", "resolved"}
	if got := weakestPrecision(allResolved); got != precisionResolved {
		t.Errorf("weakestPrecision(%v) = %q, want %q", allResolved, got, precisionResolved)
	}
	if got := weakestPrecision(nil); got != precisionResolved {
		t.Errorf("weakestPrecision(nil) = %q, want %q (vacuous case defaults to resolved)", got, precisionResolved)
	}
}

// TestAddHint_LoneHintCarriesNoLeadingSeparator pins the other half of F.5: a
// single hint attached to an empty chain must not gain a leading "; ".
func TestAddHint_LoneHintCarriesNoLeadingSeparator(t *testing.T) {
	if got := addHint("", syntacticHint); got != syntacticHint {
		t.Errorf("addHint(\"\", syntacticHint) = %q, want the bare hint with no separator", got)
	}
	if got := addHint(carriedHint, syntacticHint); got != carriedHint+"; "+syntacticHint {
		t.Errorf("addHint(carriedHint, syntacticHint) = %q, want %q", got, carriedHint+"; "+syntacticHint)
	}
}
