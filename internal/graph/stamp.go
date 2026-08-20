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
func indexedExtensions() []string {
	return []string{".go", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".py"}
}

// indexerGen is the third stampGen input: a build-semantics generation that
// changes whenever what a build MEANS changes without necessarily changing
// the schema DDL string or the indexed-extension set (design D8). PR-B
// bumped it for Go-free tolerance + the widened census walk; PR-C bumped it
// again for mapper-symbol-indexing semantics; PR-D bumps it again for
// mapper-tier call-edge-producing semantics (a repo indexed before PR-D must
// rebuild to pick up call edges, even though its schema and extension set
// are unchanged); PR-E bumps it again for its own semantics change
// (carry-forward). A schema edit or an extension-set widening already moves
// the generation on its own — this constant exists for the case neither does.
const indexerGen = "3"

// stampGen derives the stamp's generation prefix from the things that change
// what a cached graph MEANS: the schema its rows were written under, the file
// set it was built from, and the indexer's own build semantics (indexerGen).
//
// ensureFresh gates only on `stamp == meta.stamp` and graph.db carries no
// schema version, so without this a schema edit leaves every cached graph
// serving rows in the old shape until some unrelated source edit happens to
// move the stamp. Deriving the generation instead of writing it by hand means
// it cannot be forgotten: change the schema, the walk, or the indexer's
// semantics, and every graph rebuilds on the next query.
//
// The extension list is hashed in sorted order because it denotes a SET, not a
// sequence: two builds covering the same extensions must agree on the
// generation however the list was assembled. Sorting a copy also keeps the
// result deterministic if the set is ever sourced from a map — otherwise
// Go's randomised map order would change the generation on every call, no
// stamp would ever match, and every query would rebuild every graph.
func stampGen(schemaDDL string, exts []string, gen string) string {
	h := sha256.New()
	h.Write([]byte(schemaDDL))
	for _, e := range slices.Sorted(slices.Values(exts)) {
		h.Write([]byte{0})
		h.Write([]byte(e))
	}
	h.Write([]byte{0})
	h.Write([]byte(gen))
	return "v" + hex.EncodeToString(h.Sum(nil)[:4])
}

// currentGen is the live generation. Computed once: stamp() runs on every
// graph query, and all three inputs are compile-time constants.
var currentGen = stampGen(schema, indexedExtensions(), indexerGen)

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

// stamp fingerprints the repo's indexed source state: count, total size, and
// max mtime of every file under indexedExtensions() (including _test.go)
// plus Go module files (go.mod/go.sum/go.work), since dependency changes
// alter go/packages analysis and call edges. Any edit, add, or delete moves
// it. Deliberately not git-aware — uncommitted edits must invalidate the
// graph too, and the same path covers non-git repos.
//
// The census covers every indexed extension, not just .go: a mapper-only
// (.ts/.py/etc.) file change must move the stamp too, or an edit to a
// mapper-language file is silently never picked up by the next build.
//
// _test.go files are included in the census: buildIndex loads packages with
// cfg.Tests set, so _test.go declarations ARE indexed as symbols and
// participate as callers. Excluding them here (as an earlier revision did)
// would mean editing a test file never moves the stamp, so a new test caller
// is silently never picked up.
//
// Known blind spot (accepted): the count+size+maxMtime triple cannot see a
// content swap between two files that preserves the aggregate — file A takes
// B's bytes and B takes A's, keeping total count, total size, and the latest
// mtime unchanged. Since a normal edit bumps mtime to "now", this only fires
// when mtimes are also preserved (touch -r, tar/rsync --times) alongside a
// size-preserving swap: astronomically rare. Closing it would require reading
// content bytes from every indexed file on every query (stamp runs per graph
// call, not just on rebuild), so we accept the gap rather than pay that
// hot-path IO.
func stamp(repo string) (string, error) {
	exts := indexedExtensions()
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
		if !isModuleFile(name) && !slices.Contains(exts, filepath.Ext(name)) {
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
