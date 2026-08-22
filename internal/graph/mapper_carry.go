// Mapper-tier per-file carry-forward (design D6, ADR-0034 decision 8): when
// one mapper file stops parsing cleanly, serve its last-good symbols and
// edges instead of losing them outright. Go's carriedEdges (carry.go) is
// edge-only and package-keyed, valid because Go symbols survive type errors
// (the AST is still intact). A mapper file with tree-sitter ERROR nodes may
// yield no usable outline at all, so mapper carry is FILE-keyed and carries
// symbols AND edges.
package graph

import (
	"database/sql"
	"os"
	"slices"

	_ "modernc.org/sqlite"
)

// mapperCarryTrigger is the carry-forward decision (design D6, ADR-0034
// decision 8, both halves load-bearing): hasError alone over-fires on
// roughly half of break trials (a stray syntax error can leave the outline
// almost entirely intact), and defCount alone cannot distinguish a
// truncation-at-a-clean-boundary from a legitimate deletion (a file
// genuinely shrinking to fewer defs is not corruption). Only their
// conjunction — an ERROR node present AND the fresh def count falling below
// HALF the previous build's — triggers carry. The resulting miss class is a
// principled limit, not a TODO.
func mapperCarryTrigger(hasError bool, defCount, prevDefCount int) bool {
	return hasError && defCount < prevDefCount/2
}

// mapperFileHasError re-parses f a THIRD time — mapper_symbols.go's outline
// pass is the first, mapper_calls.go's collectMapperCalls FactCalls pass is
// the second — purely to read whether the resulting tree contains any ERROR
// node. mapperSym's carrier does not retain *gts.Tree (design.md decision 8),
// so no prior pass has a tree to hand over. The parse itself is pooled per
// language (mapperEngine.parsers), so the repeat costs a parse, not a parser
// construction. Best-effort: any
// read/language/parse failure reports false — carry-forward is a positive
// trigger, never the default for a file this build cannot even see.
func mapperFileHasError(engines mapperEngines, f mapperFile) bool {
	// #nosec G304 -- discovery admits only regular files under repo, size-capped
	// at maxMapperFileBytes; symlinks are dropped there so this read cannot
	// resolve outside the indexed repo.
	src, err := os.ReadFile(f.abs)
	if err != nil {
		return false
	}
	eng := engines.get(f.entry)
	if eng.lang == nil {
		return false
	}
	tree, err := eng.parsers.Parse(src) // pooled: see mapperEngine.parsers
	if err != nil {
		return false
	}
	// A parsed tree borrows node arenas from a package-level pool, and the
	// pool only refills on Release — without it every parse allocates fresh
	// arenas, which profiling showed as ~87% of this pass's bytes. Releasing
	// here is safe precisely because this function retains NOTHING derived
	// from the tree: it answers one bool and drops it. Release hands the
	// arenas to the next parse, so a caller that kept a *gts.Node, or a
	// string aliasing arena memory, would be reading recycled bytes — which
	// is why the other three mapper passes do not do this yet.
	defer tree.Release()
	root := tree.RootNode()
	if root == nil {
		return false
	}
	return root.HasError()
}

// mapperCarriedFile reads dbPath (the previous graph.db, still in place when
// buildIndex calls this) once, read-only, and returns the symbol rows it
// holds for repo-relative path rel — verbatim, as fresh *symRow values with
// id left at its zero value so buildIndex's positional-ID loop assigns it,
// exactly like a freshly-produced mapperSym row. Strictly best-effort: ANY
// failure opening, querying, or scanning dbPath yields nil, mirroring
// carriedEdges' exact contract (carry.go) — dbPath commonly does not exist
// yet (first build) and a failure here must never abort the build, only
// leave that file on its fresh (possibly broken) output.
func mapperCarriedFile(dbPath, rel string) []*symRow {
	if _, err := os.Stat(dbPath); err != nil {
		return nil // no previous graph.db (first-ever build)
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return nil
	}
	defer db.Close()

	rows, err := db.Query(`SELECT qname, name, kind, package, file, line, exported, signature, doc, source
		FROM symbols WHERE file = ?`, rel)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []*symRow
	for rows.Next() {
		var s symRow
		var exported int
		if err := rows.Scan(&s.qname, &s.name, &s.kind, &s.pkg, &s.file, &s.line,
			&exported, &s.signature, &s.doc, &s.source); err != nil {
			return nil
		}
		s.exported = exported == 1
		out = append(out, &s)
	}
	if err := rows.Err(); err != nil {
		return nil
	}
	return out
}

// mapperCarry applies the per-file trigger across every currently discovered
// mapper file, swapping in the previous build's symbol rows for any file
// whose fresh parse is too broken to trust. freshSyms is mapperSymbols' full
// output for THIS build; files is the discovery list it was built from.
//
// Returns the symbols slice to actually use in place of freshSyms — for an
// untriggered file, its fresh rows pass through unchanged; for a triggered
// file, its previous rows (id==0) are substituted wholesale — plus the
// sorted list of carried files' MODULE PATHS, not their rel paths: for a
// mapper row symRow.pkg is the extensionless module path (mapper.go's
// modulePath), and that is what query.go's per-symbol Carried check compares
// carriedUnits against (design D6's explicit "use modulePath, not rel" note,
// task E.3/E.4 — using rel here would make Carried silently never fire for a
// carried mapper symbol).
//
// The DB read (mapperCarriedFile) only runs for a file that already has an
// ERROR node — the common case (a clean file) never pays for it.
func mapperCarry(dbPath string, files []mapperFile, freshSyms []mapperSym) ([]mapperSym, []string) {
	freshByFile := map[string][]mapperSym{}
	for _, ms := range freshSyms {
		freshByFile[ms.row.file] = append(freshByFile[ms.row.file], ms)
	}

	engines := mapperEngines{}
	var carriedUnits []string
	out := make([]mapperSym, 0, len(freshSyms))

	for _, f := range files {
		fresh := freshByFile[f.rel]
		if !mapperFileHasError(engines, f) {
			out = append(out, fresh...)
			continue
		}
		prev := mapperCarriedFile(dbPath, f.rel)
		if !mapperCarryTrigger(true, len(fresh), len(prev)) {
			out = append(out, fresh...)
			continue
		}
		for _, row := range prev {
			out = append(out, mapperSym{row: row})
		}
		carriedUnits = append(carriedUnits, f.modulePath)
	}
	slices.Sort(carriedUnits)
	return out, carriedUnits
}

// mapperCarriedEdges reads dbPath (the previous graph.db) once, read-only,
// and returns edges whose CALLER symbol belongs to a carried mapper module
// (keyed by modulePath — the same key mapperCarry returns, matching
// symRow.pkg for a mapper row), remapped from the old symbol id to the fresh
// id via byQName. Mirrors carriedEdges' exact contract (carry.go): an edge
// whose caller or callee qname has no match in the fresh symbol set is
// dropped, never carried with a stale id, and dbPath commonly not existing
// yet collapses to zero edges rather than an error.
//
// A carried file's own OUTGOING edges are what this recovers — the same
// asymmetry carriedEdges documents for Go: a clean caller's edge INTO a
// carried module is already freshly rediscoverable this build (the ladder
// resolves by name against the full, carried-inclusive symbol set, needing
// no byte range from the callee side), so only the carried file's own
// callsites (whose containment info was lost along with its broken parse)
// are missing without this.
//
// T4 part 2 (design D "byQName Python collision"): an edge whose CALLEE
// qname collided at buildByQName time is DROPPED, never remapped onto the
// possibly-wrong last-wins row that collision produced — a wrong edge
// asserts a caller under an incorrect symbol identity, categorically worse
// than an absent one (the one sanctioned under-reporting exception in this
// design).
func mapperCarriedEdges(dbPath string, carriedModules map[string]bool, byQName map[string]int64, collidedQNames map[string]bool) edgeSet {
	if _, err := os.Stat(dbPath); err != nil {
		return nil // no previous graph.db (first-ever build)
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return nil
	}
	defer db.Close()

	rows, err := db.Query(`SELECT s1.qname, s1.package, s2.qname, e.dispatch, e.precision
		FROM edges e
		JOIN symbols s1 ON s1.id = e.caller
		JOIN symbols s2 ON s2.id = e.callee`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	edges := edgeSet{}
	for rows.Next() {
		var callerQName, callerPkg, calleeQName, dispatch, precision string
		if err := rows.Scan(&callerQName, &callerPkg, &calleeQName, &dispatch, &precision); err != nil {
			return nil
		}
		if !carriedModules[callerPkg] {
			continue // caller not in a carried module: already freshly rediscovered (or genuinely gone)
		}
		if collidedQNames[calleeQName] {
			continue // T4 part 2: target qname collided — drop rather than misattribute
		}
		callerID, ok := byQName[callerQName]
		if !ok {
			continue // caller symbol no longer exists in the fresh build
		}
		calleeID, ok := byQName[calleeQName]
		if !ok {
			continue // callee symbol no longer exists in the fresh build
		}
		edges[[2]int64{callerID, calleeID}] = edgeMeta{dispatch: dispatch, precision: precision}
	}
	if err := rows.Err(); err != nil {
		return nil
	}
	return edges
}
