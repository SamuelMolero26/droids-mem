package graph

import (
	"database/sql"
	"os"

	_ "modernc.org/sqlite"
)

// openPrevGraph opens dbPath read-only, bypassing Manager's refcounted
// connEntry cache entirely — the same pattern buildlock.go's stampOnDisk
// already uses. buildIndex has no *Manager, and threading one in here would
// couple a read that finishes before writeGraphDB even starts to the
// rebuild/retire lifecycle that cache exists for; the rename that publishes
// the fresh graph happens well after this handle is closed.
func openPrevGraph(dbPath string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
}

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
// Strictly best-effort: ANY error opening, querying, or scanning dbPath
// yields (nil, nil), and the build proceeds with zero carried edges. This is
// load-bearing, not defensive — dbPath commonly does not exist yet (first
// build on a broken tree) or predates the edges.dispatch schema column
// (a graph.db written by a PR1/PR2-era build): reading e.dispatch against
// that shape fails with "no such column: dispatch", which this contract
// swallows into zero carried edges rather than a buildIndex error (task 5.6).
//
// Each carried edge preserves the REAL dispatch label read from the
// previous graph.db (task 5.7) — the previous build's callEdges/carriedEdges
// already computed it correctly; carrying it forward is strictly more
// faithful than re-defaulting every carried edge to "static".
func carriedEdges(dbPath string, brokenPkgs map[string]bool, byQName map[string]int64) (edgeSet, error) {
	if len(brokenPkgs) == 0 {
		return nil, nil
	}
	// Every branch below returns the literal (nil, nil) on failure, never the
	// captured err — carry-forward is best-effort by contract, so a failure
	// here is reported as "zero carried edges", not as a buildIndex error.
	if _, err := os.Stat(dbPath); err != nil {
		return nil, nil // no previous graph.db (first-ever build on a broken tree)
	}

	db, err := openPrevGraph(dbPath)
	if err != nil {
		return nil, nil // open failure: unreadable or corrupt previous graph.db
	}
	defer db.Close()

	rows, err := db.Query(`SELECT s1.qname, s1.package, s2.qname, e.dispatch
		FROM edges e
		JOIN symbols s1 ON s1.id = e.caller
		JOIN symbols s2 ON s2.id = e.callee`)
	if err != nil {
		return nil, nil // e.g. a pre-dispatch-column schema (task 5.6)
	}
	defer rows.Close()

	edges := edgeSet{}
	for rows.Next() {
		var callerQName, callerPkg, calleeQName, dispatch string
		if err := rows.Scan(&callerQName, &callerPkg, &calleeQName, &dispatch); err != nil {
			return nil, nil
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
		edges[[2]int64{callerID, calleeID}] = dispatch
	}
	if err := rows.Err(); err != nil {
		return nil, nil
	}
	return edges, nil
}
