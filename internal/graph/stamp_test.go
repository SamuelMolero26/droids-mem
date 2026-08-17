package graph

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestStamp_TestFileEditMovesStamp pins the Tests:true lockstep requirement
// (stamp.go's own comment already said this filter must drop "in lockstep"
// once test indexing is enabled): once the semantic tier loads packages with
// Tests:true, declarations inside _test.go files are indexed as symbols and
// participate as callers, so an edit to a _test.go file that adds a new
// caller MUST move the build stamp — otherwise a new test caller is silently
// never picked up because the cache never invalidates.
func TestStamp_TestFileEditMovesStamp(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "go.mod", "module x\n\ngo 1.21\n")
	writeFile(t, repo, "a.go", "package x\n\nfunc A() {}\n")
	writeFile(t, repo, "a_test.go", "package x\n")

	base, err := stamp(repo)
	if err != nil {
		t.Fatal(err)
	}

	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(filepath.Join(repo, "a_test.go"), future, future); err != nil {
		t.Fatal(err)
	}
	if s, err := stamp(repo); err != nil {
		t.Fatal(err)
	} else if s == base {
		t.Errorf("test-file edit did not move stamp: still %q; with Tests:true a _test.go edit must invalidate the cache", base)
	}
}
