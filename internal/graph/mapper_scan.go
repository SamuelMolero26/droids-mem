// mapper_scan.go holds the mapper tier's single-parse driver.
//
// The four mapper passes (symbols, calls, imports, carry's ERROR probe) each
// used to open, read and parse every discovered file on their own — four
// os.ReadFile + Parse round trips over identical bytes. Each pass had a
// defensible local reason (gts.Parser carries reuse state and is not safe to
// share, and no pass returned its tree), but the second half of that reason
// was self-referential: no pass returned a tree because every pass parsed its
// own.
//
// Profiling a 200-file cold build put os.ReadFile at 52.07% of total CPU —
// syscall.Open alone was 1.27s of a 1.36s syscall total — against 13.22% for
// ParserPool.Parse. The dominant cost was never parsing; it was opening the
// same files four times. A further 7.85% went to gts.NewParser rebuilding
// parse tables, because each pass built its OWN mapperEngines and therefore
// its own ParserPool, so the pool had nothing to hand back.
//
// Both collapse into the same fix: one loop, one engines map, one read, one
// parse, every extraction run against that single tree.
package graph

import "os"

// mapperScanResult is everything the four mapper passes produce, gathered in
// one traversal. Field-for-field it is what mapperSymbols, collectMapperCalls,
// mapperImports and the carry ERROR probe returned separately.
type mapperScanResult struct {
	syms       []mapperSym
	fileCalls  []mapperFileCalls
	importRows []importRow
	bindings   mapperImportBindings
	// hasError is keyed by rel path and holds only the true entries — a file
	// absent from the map parsed clean. Feeds mapperCarryScanned.
	hasError map[string]bool
	stats    mapperStats
}

// scanMapperFiles reads and parses every file in files EXACTLY ONCE and runs
// all four mapper extractions against that single tree.
//
// One mapperEngines for the whole scan, not one per pass: the map is still
// per-build and never package-level, so design decision 3's "Manager builds
// different repos concurrently, a shared cache would need locking for no
// gain" holds exactly as before — this only stops the same build from
// rebuilding its own parse tables four times over.
//
// Per-file failures stay skip-and-continue, matching every pass's existing
// policy. Counting differs in one harmless way: an unreadable or unparsable
// file now increments its stat once for the build rather than once per pass.
// buildIndex discards mapper stats, and the per-pass entry points still count
// the old way for the tests that assert on them.
func scanMapperFiles(files []mapperFile) mapperScanResult {
	res := mapperScanResult{
		bindings: mapperImportBindings{},
		hasError: map[string]bool{},
	}
	engines := mapperEngines{}

	for _, f := range files {
		// #nosec G304 -- discovery admits only regular files under repo, size-capped
		// at maxMapperFileBytes; symlinks are dropped there so this read cannot
		// resolve outside the indexed repo.
		src, err := os.ReadFile(f.abs)
		if err != nil {
			res.stats.readErr++
			continue // unreadable file is skip-and-continue, not fatal
		}
		eng := engines.get(f.entry)
		if eng.lang == nil {
			res.stats.parseErr++ // no working language: no tree can ever be produced
			continue
		}
		scanMapperFile(eng, f, src, &res)
	}
	return res
}

// scanMapperFile is one file's single parse plus every extraction, scoped as
// its own function so `defer tree.Release()` runs on each exit path instead of
// queueing behind the whole scan — the same shape, and the same reason, as the
// per-pass helpers it replaces. Release returns the node arenas to the pool for
// the next file; holding them to the end of the scan would defeat the pool
// entirely.
//
// Every extraction below is documented release-safe at its own definition:
// nothing retained here points into the tree's arenas.
func scanMapperFile(eng *mapperEngine, f mapperFile, src []byte, res *mapperScanResult) {
	tree, err := eng.parsers.Parse(src) // pooled: see mapperEngine.parsers
	if err != nil {
		res.stats.parseErr++
		return // unparsable file is skip-and-continue, not fatal
	}
	defer tree.Release()

	res.syms = append(res.syms, outlineMapperTree(eng, f, src, tree, &res.stats)...)

	if refs := callsFromMapperTree(eng, f, src, tree, &res.stats); len(refs) > 0 {
		res.fileCalls = append(res.fileCalls, mapperFileCalls{file: f.rel, lang: f.entry.Name, refs: refs})
	}

	// Imports cover Python and the JS family only; every other mapper language
	// has no import pass at all (mapper_imports.go's package doc).
	if f.entry != nil {
		isPython := f.entry.Name == "python"
		if isPython || jsFamilyLanguages[f.entry.Name] {
			importsFromMapperTree(eng, f, src, tree, isPython, &res.importRows, res.bindings, &res.stats)
		}
	}

	// Carry's trigger input, read off the tree already in hand rather than by
	// the third re-parse mapperFileHasError used to do.
	if root := tree.RootNode(); root != nil && root.HasError() {
		res.hasError[f.rel] = true
	}
}
