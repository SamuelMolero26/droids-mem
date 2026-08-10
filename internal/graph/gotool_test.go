package graph

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// lookGoIn reports whether any PATH entry in env holds a go executable.
func lookGoIn(t *testing.T, env []string) bool {
	t.Helper()
	path := ""
	for _, kv := range env {
		if after, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = after // last assignment wins, as in exec
		}
	}
	for _, dir := range filepath.SplitList(path) {
		if _, err := os.Stat(filepath.Join(dir, "go")); err == nil {
			return true
		}
	}
	return false
}

// A `go` already on PATH needs no help: inherit the process environment
// unchanged so the caller's own toolchain selection is never overridden.
func TestGoToolEnv_InheritsWhenGoOnPath(t *testing.T) {
	if env := goToolEnv(); env != nil {
		t.Fatalf("goToolEnv() = %d entries, want nil (inherit) when go is on PATH", len(env))
	}
}

// A server spawned detached (launchd, login item) inherits a PATH without the
// Go toolchain and could never rebuild any graph — go/packages shells out to
// `go list`. Fall back to the GOROOT baked in at build time (#70).
func TestGoToolEnv_FallsBackToBuildGOROOT(t *testing.T) {
	//nolint:staticcheck // runtime.GOROOT is deprecated, but it is the only
	// toolchain location available precisely when PATH has no `go` to ask.
	goBin := filepath.Join(runtime.GOROOT(), "bin")
	if _, err := os.Stat(filepath.Join(goBin, "go")); err != nil {
		t.Skipf("no go at build GOROOT (%s)", goBin)
	}
	t.Setenv("PATH", t.TempDir()) // a PATH with no toolchain on it

	env := goToolEnv()
	if env == nil {
		t.Fatal("goToolEnv() = nil, want an environment carrying the GOROOT toolchain")
	}
	if !lookGoIn(t, env) {
		t.Fatal("goToolEnv() returned an environment whose PATH still has no go")
	}
}
