package pkgtest

import "testing"

// TestReal is exported and lives in a _test.go file: the package surface must
// count it, never list it.
func TestReal(t *testing.T) {
	if Real() != 1 {
		t.Fatal("Real")
	}
}

// HelperFixture is a second exported _test.go symbol, so the tests count is
// distinguishable from "exactly one".
func HelperFixture() {}
