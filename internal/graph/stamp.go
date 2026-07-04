package graph

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

// stamp fingerprints the repo's Go source state: count, total size, and max
// mtime of every non-test .go file plus module files (go.mod/go.sum/go.work),
// since dependency changes alter go/packages analysis and call edges. Any edit,
// add, or delete moves it. Deliberately not git-aware — uncommitted edits must
// invalidate the graph too, and the same path covers non-git repos.
//
// _test.go files are excluded: buildIndex loads packages with cfg.Tests unset,
// so test files are never indexed (verified: 0 test symbols). Rebuilding on a
// test edit would burn a full ~2.5s type-check for a graph that can't change.
// If test indexing is ever enabled, drop the _test.go filter here in lockstep.
func stamp(repo string) (string, error) {
	var count int
	var size, maxMtime int64
	err := filepath.WalkDir(repo, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // unreadable entries don't invalidate the walk
		}
		if d.IsDir() {
			name := d.Name()
			if p != repo && (strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules") {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !isModuleFile(name) && (!strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go")) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr // racing deletes don't invalidate the walk
		}
		count++
		size += info.Size()
		if mt := info.ModTime().UnixNano(); mt > maxMtime {
			maxMtime = mt
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("stamp %s: %w", repo, err)
	}
	return fmt.Sprintf("v1:%d:%d:%d", count, size, maxMtime), nil
}

// isModuleFile reports whether name is a Go module manifest whose changes can
// alter package resolution and thus the call graph.
func isModuleFile(name string) bool {
	return name == "go.mod" || name == "go.sum" || name == "go.work" || name == "go.work.sum"
}
