package graph

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// indexedExtensions is the source-file set the index is built from. It is an
// input to the stamp generation, so widening it invalidates every cached
// graph — a graph built before the set grew was built from fewer files.
func indexedExtensions() []string {
	return []string{".go", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".cts", ".mts", ".py"}
}

// indexerGen is the third stampGen input: a build-semantics generation that
// changes whenever what a build MEANS changes without necessarily changing
// the schema DDL string or the indexed-extension set (design D8). PR-B
// bumped it for Go-free tolerance + the widened census walk; PR-C bumped it
// again for mapper-symbol-indexing semantics; PR-D bumped it again for
// mapper-tier call-edge-producing semantics; PR-E bumps it again for
// per-file carry-forward semantics (a repo indexed before PR-E must rebuild
// for a broken mapper file to start carrying its previous symbols/edges
// forward instead of losing them outright, even though its schema and
// extension set are unchanged). A schema edit or an extension-set widening
// already moves the generation on its own — this constant exists for the
// case neither does.
//
// PR-G1 deliberately did NOT bump it: TS/JS import rows are additive and
// nothing reads the imports table, so a pre-G1 graph answered no query
// differently. PR-G2 bumps it because ladder rung 2a changes which EDGES a
// build writes — a graph indexed before it holds edges resolved without
// import scoping, which is not merely less complete but differently
// attributed. That is the line: bump when a stored graph becomes wrong, not
// when it merely lacks data no one reads. P2 (alias) bumps it again because
// "@/..." imports previously fell to the lossy repo-wide rung 5 instead of
// the precise import-scoped rung 2a. Bare and namespace import narrowing also
// changes stored edge attribution, so it advances the generation once more.
// Generation 8 carries file directives with last-good mapper symbols instead
// of writing metadata from an untrustworthy current parse. Generation 9 adds
// meta.tests_skipped: a graph built before it has no such row, so the
// answer omits the disclosure — a silent under-report on any mapper repo with
// tests. That is the bump line exactly: the stored graph became wrong, not
// merely incomplete. Generation 10 merges repeated Python qnames into one row
// and indexes TS type aliases and Python module-level bindings: a generation-9
// graph holds duplicate rows that dead-end graph_symbol.
const indexerGen = "10"

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
	case "vendor", "node_modules", "dist", "build", "target", "__pycache__", "out":
		return true
	}
	return false
}

// stamp fingerprints the repo's indexed source state: count, total size, and
// max mtime of every file under indexedExtensions() (including _test.go)
// plus Go module files (go.mod/go.sum/go.work) and the exact alias-config
// inputs loadAliasConfig can consume, since both dependency and alias changes
// alter call edges. Any normal edit, add, or delete moves it. Deliberately not
// git-aware — uncommitted edits must invalidate the graph too, and the same
// path covers non-git repos.
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
// The aggregate remains readable in the stamp, but invalidation uses a second
// deterministic digest over each input's repo-relative path, size, and mtime.
// This detects path renames and add/delete swaps without reading source bytes
// on the query hot path.
//
// Inputs are hashed as they are visited, so memory stays O(1) in the number
// of files. WalkDir visits in lexical order, which makes the digest
// deterministic for a given tree without collecting and sorting the census.
func stamp(repo string) (string, error) {
	exts := indexedExtensions()
	h := sha256.New()
	var count int
	var size, maxMtime int64
	add := func(name string, info fs.FileInfo) {
		rel, err := filepath.Rel(repo, name)
		if err != nil {
			rel = name
		}
		mtime := info.ModTime().UnixNano()
		count++
		size += info.Size()
		maxMtime = max(maxMtime, mtime)
		fmt.Fprintf(h, "%s\x00%d\x00%d\x00", filepath.ToSlash(rel), info.Size(), mtime)
	}
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
		add(p, info)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("stamp %s: %w", repo, err)
	}
	// Alias configs are .json, so the census walk above can never have counted
	// one — but aliasConfigFiles may name the same path twice (a config that
	// extends a sibling which extends it back), so dedupe within that short
	// slice rather than tracking every walked file.
	for _, p := range slices.Compact(slices.Sorted(slices.Values(aliasConfigFiles(repo)))) {
		info, err := os.Stat(p) // #nosec G304 -- fixed config names under the checkout, discovered by aliasConfigFiles
		if err != nil {
			continue
		}
		add(p, info)
	}
	digest := hex.EncodeToString(h.Sum(nil)[:8])
	return fmt.Sprintf("%s:%d:%d:%d:%s", currentGen, count, size, maxMtime, digest), nil
}

// isModuleFile reports whether name is a Go module manifest whose changes can
// alter package resolution and thus the call graph.
func isModuleFile(name string) bool {
	return name == "go.mod" || name == "go.sum" || name == "go.work" || name == "go.work.sum"
}
