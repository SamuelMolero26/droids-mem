package store

import (
	"strings"
	"testing"
)

func TestGrowthWarnings_UnderThresholds(t *testing.T) {
	if w := growthWarnings(1024, 100); w != nil {
		t.Errorf("expected no warnings, got %v", w)
	}
	// exactly at threshold is still fine — warnings fire strictly above
	if w := growthWarnings(warnDBSizeBytes, warnRowCount); w != nil {
		t.Errorf("expected no warnings at exact thresholds, got %v", w)
	}
}

func TestGrowthWarnings_OverDBSize(t *testing.T) {
	w := growthWarnings(warnDBSizeBytes+1, 100)
	if len(w) != 1 {
		t.Fatalf("warnings = %d, want 1", len(w))
	}
	if !strings.Contains(w[0], "prune --suggest-dupes") {
		t.Errorf("size warning should point at prune --suggest-dupes, got %q", w[0])
	}
}

func TestGrowthWarnings_OverRowCount(t *testing.T) {
	w := growthWarnings(1024, warnRowCount+1)
	if len(w) != 1 {
		t.Fatalf("warnings = %d, want 1", len(w))
	}
	if !strings.Contains(w[0], "near-duplicate") {
		t.Errorf("row warning should mention near-duplicate tuning, got %q", w[0])
	}
}

func TestGrowthWarnings_BothOver(t *testing.T) {
	if w := growthWarnings(warnDBSizeBytes+1, warnRowCount+1); len(w) != 2 {
		t.Fatalf("warnings = %d, want 2", len(w))
	}
}
