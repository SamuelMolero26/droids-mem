package tui

import (
	"fmt"
	"os"
	"testing"
)

// TestMain points DROIDS_MEM_HOME at a throwaway dir for the whole package.
//
// This is a data-loss guard, not tidiness. shareCmd persists the pool path via
// state.SaveShareRepo once push succeeds, and the share tests stub push to
// succeed, so any test that drives the share flow without an isolated home
// overwrites the developer's real ~/.droids-mem/share_repo with a path under
// the source tree. Nothing errors: the next `serve` boot auto-Fetch just
// resolves a directory that does not exist and logs "boot fetch skipped"
// forever, and the pool the developer had configured is gone.
//
// Isolating per test is one t.Setenv that is easy to forget — and its absence
// is invisible until someone inspects their own state dir. Isolating the
// package makes forgetting impossible.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "droids-mem-tui")
	if err != nil {
		fmt.Fprintf(os.Stderr, "tui tests: create temp home: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("DROIDS_MEM_HOME", dir); err != nil {
		fmt.Fprintf(os.Stderr, "tui tests: set DROIDS_MEM_HOME: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
