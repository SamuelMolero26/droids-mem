package graph

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

// stamp fingerprints the repo's Go source state: count, total size, and max
// mtime of every .go file plus module files (go.mod/go.sum/go.work), since
// dependency changes alter go/packages analysis and call edges. Any edit, add,
// or delete moves it. Deliberately not git-aware — uncommitted edits must
// invalidate the graph too, and the same path covers non-git repos.
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
		if !strings.HasSuffix(p, ".go") && !isModuleFile(d.Name()) {
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
