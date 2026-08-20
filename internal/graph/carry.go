package graph

import (
	"database/sql"
	"os"

	_ "modernc.org/sqlite"
)

// carriedEdges reads dbPath (the previous graph.db, still in place at the
// time buildIndex calls this — writeGraphDB has not renamed the fresh one
// over it yet) once, read-only, and returns edges whose CALLER symbol
// belongs to a broken package (keyed by short package name, matching
// symRow.pkg / the persisted `package` column), remapped from the old
// symbol id to the fresh id via byQName. An edge whose caller or callee
// qname has no match in the fresh symbol set is dropped, never carried with
// a stale id. Cross-unit edges (clean caller -> broken callee) are never
// carried: only a broken package's own OUTGOING edges are missing from a
// fresh build (its functions have no SSA body), so only those are worth
// recovering — an in-edge from a clean caller was already freshly
// rediscovered by callEdges (or genuinely no longer exists).
//
// The read opens dbPath directly, bypassing Manager's refcounted connEntry
// cache — the same pattern buildlock.go's stampOnDisk already uses.
// buildIndex has no *Manager, and threading one in here would couple a read
// that finishes before writeGraphDB even starts to the rebuild/retire
// lifecycle that cache exists for.
//
// Strictly best-effort, so it returns no error at all: ANY failure opening,
// querying, or scanning dbPath yields nil, and the build proceeds with zero
// carried edges. This is load-bearing, not defensive — dbPath commonly does
// not exist yet (first build on a broken tree) or predates the
// edges.dispatch schema column (a graph.db written by a PR1/PR2-era build):
// reading e.dispatch against that shape fails with "no such column:
// dispatch", which this contract swallows into zero carried edges rather
// than a buildIndex error (task 5.6).
//
// Each carried edge preserves the REAL dispatch AND precision labels read
// from the previous graph.db (task 5.7, widened by task A.5) — the previous
// build already computed them correctly; carrying them forward is strictly
// more faithful than re-defaulting every carried edge to "static"/"resolved".
func carriedEdges(dbPath string, brokenPkgs map[string]bool, byQName map[string]int64) edgeSet {
	if _, err := os.Stat(dbPath); err != nil {
		return nil // no previous graph.db (first-ever build on a broken tree)
	}

	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return nil // open failure: unreadable or corrupt previous graph.db
	}
	defer db.Close()

	rows, err := db.Query(`SELECT s1.qname, s1.package, s2.qname, e.dispatch, e.precision
		FROM edges e
		JOIN symbols s1 ON s1.id = e.caller
		JOIN symbols s2 ON s2.id = e.callee`)
	if err != nil {
		return nil // e.g. a pre-dispatch-column schema (task 5.6)
	}
	defer rows.Close()

	edges := edgeSet{}
	for rows.Next() {
		var callerQName, callerPkg, calleeQName, dispatch, precision string
		if err := rows.Scan(&callerQName, &callerPkg, &calleeQName, &dispatch, &precision); err != nil {
			return nil
		}
		if !brokenPkgs[callerPkg] {
			continue // caller in a clean package: already freshly rediscovered (or genuinely gone)
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
