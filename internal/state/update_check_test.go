package state

import "testing"

func TestUpdateCheckCache_RoundTripAndTTL(t *testing.T) {
	t.Setenv("DROIDS_MEM_HOME", t.TempDir())

	if _, ok := CachedLatestVersion(); ok {
		t.Fatal("expected cold cache miss")
	}

	if err := WriteLatestVersion("1.3.0"); err != nil {
		t.Fatalf("WriteLatestVersion: %v", err)
	}

	got, ok := CachedLatestVersion()
	if !ok || got != "1.3.0" {
		t.Fatalf("CachedLatestVersion() = (%q, %v), want (1.3.0, true)", got, ok)
	}
}
