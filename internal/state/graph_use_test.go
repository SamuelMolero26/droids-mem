package state

import (
	"testing"
	"time"
)

func TestGraphUse_RoundTripAndExpiry(t *testing.T) {
	t.Setenv("DROIDS_MEM_HOME", t.TempDir())

	if tool, ok := LastGraphUse(time.Minute); ok {
		t.Fatalf("no marker written yet, got %q", tool)
	}

	RecordGraphUse("graph_symbol")
	tool, ok := LastGraphUse(time.Minute)
	if !ok || tool != "graph_symbol" {
		t.Fatalf("LastGraphUse = %q, %v; want graph_symbol, true", tool, ok)
	}

	// A marker older than the window is not a current indicator.
	if _, ok := LastGraphUse(-time.Second); ok {
		t.Error("expired marker should report no recent use")
	}
}
