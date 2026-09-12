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
// decision 8, both halves load-bearing): an untrustworthy scan alone
// over-fires on roughly half of break trials (a stray syntax error can leave
// the outline almost entirely intact), and defCount alone cannot distinguish a
// truncation-at-a-clean-boundary from a legitimate deletion (a file
// genuinely shrinking to fewer defs is not corruption). Only their
// conjunction — a read/parser failure or ERROR node AND the fresh def count
// falling below HALF the previous build's — triggers carry. The resulting
// miss class is a principled limit, not a TODO.
func mapperCarryTrigger(hasError bool, defCount, prevDefCount int) bool {
	return hasError && defCount < prevDefCount/2
}

// mapperCarriedFile reads dbPath (the previous graph.db, still in place when
// buildIndex calls this) once, read-only, and returns the symbol rows it
// holds for repo-relative path rel — verbatim, as fresh *symRow values with
// id left at its zero value so buildIndex's positional-ID loop assigns it,
// exactly like a freshly-produced mapperSym row — plus its file directive.
// Strictly best-effort: ANY failure opening, querying, or scanning dbPath
// yields no prior state, mirroring carriedEdges' exact contract (carry.go).
func mapperCarriedFile(dbPath, rel string) ([]*symRow, string) {
	if _, err := os.Stat(dbPath); err != nil {
		return nil, "" // no previous graph.db (first-ever build)
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return nil, ""
	}
	defer db.Close()

	rows, err := db.Query(`SELECT qname, name, kind, package, file, line, exported, signature, doc, source
		FROM symbols WHERE file = ?`, rel)
	if err != nil {
		return nil, ""
	}
	defer rows.Close()

	var out []*symRow
	for rows.Next() {
		var s symRow
		var exported int
		if err := rows.Scan(&s.qname, &s.name, &s.kind, &s.pkg, &s.file, &s.line,
			&exported, &s.signature, &s.doc, &s.source); err != nil {
			return nil, ""
		}
		s.exported = exported == 1
		out = append(out, &s)
	}
	if err := rows.Err(); err != nil {
		return nil, ""
	}
	// Unlike the query path, a missing file_directives table IS reachable here:
	// this reads the PREVIOUS graph.db, which on the first build after a schema
	// change was written under the older DDL. No directive to carry is a normal
	// outcome, so every failure means the same thing — carry the symbols alone.
	var directive string
	_ = db.QueryRow(`SELECT directive FROM file_directives WHERE file = ?`, rel).Scan(&directive)
	return out, directive
}

// mapperCarry applies the per-file trigger across every currently discovered
// mapper file, swapping in the previous build's symbol rows for any file
// whose fresh parse is too broken to trust. freshSyms is the scan's full
// symbol output for THIS build; files is the discovery list it was built
// from; hasError is the scan's per-file trust verdict, read off the SAME tree
// that produced freshSyms — so the build never re-parses to get it.
//
// Returns the symbols slice to actually use in place of freshSyms — for an
// untriggered file, its fresh rows pass through unchanged; for a triggered
// file, its previous rows (id==0) are substituted wholesale — plus the
// sorted list of carried files' MODULE PATHS, not their rel paths: for a
// mapper row symRow.pkg is the extensionless module path (mapper.go's
// modulePath), and that is what query.go's per-symbol Carried check compares
// carriedUnits against (design D6's explicit "use modulePath, not rel" note,
// task E.3/E.4 — using rel here would make Carried silently never fire for a
// carried mapper symbol) — plus each carried file's last-good directive.
//
// The DB read (mapperCarriedFile) only runs for a file whose scan is already
// untrustworthy — the common case (a clean file) never pays for it.
func mapperCarry(dbPath string, files []mapperFile, freshSyms []mapperSym, hasError map[string]bool) ([]mapperSym, []string, map[string]string) {
	freshByFile := map[string][]mapperSym{}
	for _, ms := range freshSyms {
		freshByFile[ms.row.file] = append(freshByFile[ms.row.file], ms)
	}

	var carriedUnits []string
	carriedDirectives := map[string]string{}
	out := make([]mapperSym, 0, len(freshSyms))

	for _, f := range files {
		fresh := freshByFile[f.rel]
		if !hasError[f.rel] {
			out = append(out, fresh...)
			continue
		}
		prev, directive := mapperCarriedFile(dbPath, f.rel)
		if !mapperCarryTrigger(true, len(fresh), len(prev)) {
			out = append(out, fresh...)
			continue
		}
		for _, row := range prev {
			out = append(out, mapperSym{row: row})
		}
		carriedUnits = append(carriedUnits, f.modulePath)
		carriedDirectives[f.rel] = directive
	}
	slices.Sort(carriedUnits)
	return out, carriedUnits, carriedDirectives
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
