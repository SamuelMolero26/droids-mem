package graph

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// goToolEnv returns the environment for packages.Load, or nil to inherit the
// process environment unchanged.
//
// go/packages shells out to `go list`, so a process whose PATH lacks the Go
// toolchain can never build a graph — it fails every time with `exec: "go":
// executable file not found in $PATH` and serves stale forever. That is not
// hypothetical: `serve` is usually spawned detached and copies the environment
// of whatever started it, which for launchd, a login item, or a non-interactive
// shell is often a bare /usr/bin:/bin PATH (#70).
//
// The fallback is the GOROOT baked in at build time — deprecated to read, but
// it is the only toolchain location available precisely when there is no `go`
// on PATH to ask. When that GOROOT holds no toolchain either (binary copied to
// another machine), inherit unchanged and let the load report its own error.
func goToolEnv() []string {
	if _, err := exec.LookPath("go"); err == nil {
		return nil
	}
	//nolint:staticcheck // see above: no `go` on PATH means `go env GOROOT` is
	// not reachable, so the build-time GOROOT is the only hint left.
	goBin := filepath.Join(runtime.GOROOT(), "bin")
	if _, err := os.Stat(filepath.Join(goBin, "go")); err != nil {
		return nil
	}
	return append(os.Environ(), "PATH="+goBin+string(os.PathListSeparator)+os.Getenv("PATH"))
}
