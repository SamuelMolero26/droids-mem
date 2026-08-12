package graph

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
)

// indexedExtensions is the source-file set the index is built from. It is an
// input to the stamp generation, so widening it invalidates every cached
// graph — a graph built before the set grew was built from fewer files.
func indexedExtensions() []string { return []string{".go"} }

// stampGen derives the stamp's generation prefix from the things that change
// what a cached graph MEANS: the schema its rows were written under, and the
// file set it was built from.
//
// ensureFresh gates only on `stamp == meta.stamp` and graph.db carries no
// schema version, so without this a schema edit leaves every cached graph
// serving rows in the old shape until some unrelated source edit happens to
// move the stamp. Deriving the generation instead of writing it by hand means
// it cannot be forgotten: change the schema or the walk, and every graph
// rebuilds on the next query.
//
// The extension list is hashed in sorted order because it denotes a SET, not a
// sequence: two builds covering the same extensions must agree on the
// generation however the list was assembled. Sorting a copy also keeps the
// result deterministic if the set is ever sourced from a map — otherwise
// Go's randomised map order would change the generation on every call, no
// stamp would ever match, and every query would rebuild every graph.
func stampGen(schemaDDL string, exts []string) string {
	h := sha256.New()
	h.Write([]byte(schemaDDL))
	for _, e := range slices.Sorted(slices.Values(exts)) {
		h.Write([]byte{0})
		h.Write([]byte(e))
	}
	return "v" + hex.EncodeToString(h.Sum(nil)[:4])
}

// currentGen is the live generation. Computed once: stamp() runs on every
// graph query, and both inputs are compile-time constants.
var currentGen = stampGen(schema, indexedExtensions())

// skipDir reports whether a directory is excluded from the source walk.
// Dotdirs cover .git/.venv; the rest are build output and dependency trees,
// which are not source: walking them moves the stamp on every build, and once
// the index covers more than .go it would index generated code.
func skipDir(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch name {
	case "vendor", "node_modules", "dist", "build", "target", "__pycache__":
		return true
	}
	return false
}

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
//
// Known blind spot (accepted): the count+size+maxMtime triple cannot see a
// content swap between two files that preserves the aggregate — file A takes
// B's bytes and B takes A's, keeping total count, total size, and the latest
// mtime unchanged. Since a normal edit bumps mtime to "now", this only fires
// when mtimes are also preserved (touch -r, tar/rsync --times) alongside a
// size-preserving swap: astronomically rare. Closing it would require reading
// content bytes from every .go file on every query (stamp runs per graph call,
// not just on rebuild), so we accept the gap rather than pay that hot-path IO.
func stamp(repo string) (string, error) {
	var count int
	var size, maxMtime int64
	err := filepath.WalkDir(repo, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // unreadable entries don't invalidate the walk
		}
		if d.IsDir() {
			if p != repo && skipDir(d.Name()) {
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
	return fmt.Sprintf("%s:%d:%d:%d", currentGen, count, size, maxMtime), nil
}

// isModuleFile reports whether name is a Go module manifest whose changes can
// alter package resolution and thus the call graph.
func isModuleFile(name string) bool {
	return name == "go.mod" || name == "go.sum" || name == "go.work" || name == "go.work.sum"
}
