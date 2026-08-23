package graph

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
)

const (
	maxDepth      = 5
	maxPathDepth  = 10
	maxMatches    = 20
	maxPkgSymbols = 200
	// Hints are surface-neutral (ADR-0027): they name the action + the qname to
	// re-query with, never a specific invocation ("graph_symbol" vs
	// "droids-mem graph symbol"). The agent already holds the surface it just
	// called; the qname is the only missing payload and it is in the rows.
	expandHint    = "neighbors are signatures only; re-query a neighbor's exact qname to see its body"
	ambiguousHint = "multiple symbols share that name; re-query with one of the qnames in matches"
	searchHint    = "no exact symbol match; these are the closest by relevance — re-query with one qname from matches for its full body, callers, and callees"
	maxSeeds      = 10  // search-fallback menu size
	blastCap      = 500 // transitive-caller count sentinel (compute + number guard)
	// staleGraphHint no longer claims the repo "no longer type-checks" — Stale
	// also covers a benign in-flight rebuild with no failure at all (a partial
	// build that itself succeeds is never stale; see graph.go's Freshness doc).
	// freshness.index_error, when present, carries the real failure reason.
	staleGraphHint  = "graph is stale: serving the last good index while it updates (see freshness.index_error if the last build failed)"
	pkgSymbolsLimit = "exported symbols only; re-query an unexported symbol by its name"
	// blast radius rides entirely on call edges, and only func/method symbols are
	// edge endpoints (byPos maps FuncDecls only). So transitive_callers is a
	// structural 0 for a type/const/var — omitting it (issue #47) stops an agent
	// reading 0 as "safe to change, nothing uses it". The hint redirects to the
	// path that does carry the answer: a type WITH methods points at them
	// (blastTypeHint); a const/var or a method-less type has no call-graph handle
	// at all, so its uses are reference-level and unindexed (blastRefHint).
	blastTypeHint = "transitive_callers is a call-edge metric (func/method); for a type, query its methods with direction=up to gauge dependents"
	blastRefHint  = "transitive_callers is a call-edge metric (func/method); this symbol has no call edges, and reference-level usage is not indexed"
	// implementers ARE the blast radius of a method-signature change on an
	// interface — the exact must-update set (issue #48), not the call-edge
	// closure. Scoped so implementers_total:0 reads as "no REPO implementer",
	// never "implements nothing" (a repo type may still satisfy a stdlib iface).
	implementersHint = "implementers are the concrete types satisfying this interface — the exact set a method-signature change must update (repo-defined types only; stdlib/dependency implementers are not indexed); re-query one by its qname for its body"
	// truncatedHint fires when a neighbor list hit maxNeighbors (issue #49). The
	// retained slice is same-package-first then alphabetical — a partial slice,
	// NOT the closest callers. callers_total/callees_total carry the real count
	// (depth=1 only). Redirect: narrow with direction+depth=1 or graph_package.
	truncatedHint  = "neighbor list is a partial slice at the cap (see *_total), not the closest — narrow with a single direction at depth=1, or graph_package"
	rebuildingHint = "graph is being rebuilt asynchronously — use graph_build_wait to block until ready, or retry"
	// carriedHint names WHICH half of a carried answer is degraded. Carried
	// alone is a bare fact, and the safe reading of a bare fact is to distrust
	// the whole response — which discards a freshly-analyzed caller list, the
	// exact payload a blast-radius query asked for. Only callees ride on the
	// previous build: a broken package's functions have no SSA body, so its
	// OUT-edges cannot be rediscovered, while its symbols come from the AST
	// and its in-edges resolve through the types-only stub.
	carriedHint = "this symbol's package did not type-check, so its callees are carried forward from the previous build and may be out of date; the symbol, its signature, and its callers are freshly analyzed"
	// syntacticHint tells the agent the numbers/edges just reported are
	// mapper-tier: name+containment heuristics (design D4), not type-checked
	// call resolution. Appended after carriedHint/rebuildingHint and before
	// the later truncatedHint append — see the ordered chain comment in
	// Symbol() (design D5/D7). Attached only when Precision == "syntactic";
	// a "resolved" (Go) answer needs no caveat.
	syntacticHint = "this symbol is mapper-tier (non-Go): its callers/callees are syntactically resolved from names and lexical containment, not type-checked — treat an ambiguous call site's candidates as approximate, not exact"
	// precisionResolved/precisionSyntactic name SymbolResponse.Precision's two
	// values (design D7). The rest of the mapper tier (edgeSet, mapper_calls.go)
	// uses the same two values as bare string literals; named here because
	// query.go's derivation and weakestPrecision below compare against them
	// repeatedly.
	precisionResolved  = "resolved"
	precisionSyntactic = "syntactic"
)

// maxNeighbors caps neighbors per direction across all depths. A var, not a
// const, purely so tests can shrink the cap and exercise truncation against a
// small fixture instead of a 51-caller one.
var maxNeighbors = 50

// SymbolRequest is a symbol-anchored query (ADR-0020 tool 1): point lookup,
// blast radius (direction=up, depth>1), or call path (To set).
type SymbolRequest struct {
	Repo      string
	Symbol    string
	Direction string // up | down | both (default both)
	Depth     int    // 1..maxDepth, default 1
	To        string // optional path target
}

// SymbolInfo is the full always-tier body of the queried symbol.
type SymbolInfo struct {
	QName     string `json:"qname"`
	Kind      string `json:"kind"`
	Package   string `json:"package"`
	File      string `json:"file"`
	Line      int    `json:"line"`
	Signature string `json:"signature"`
	Doc       string `json:"doc,omitempty"`
	Source    string `json:"source,omitempty"`
}

// Neighbor is a browse-tier stub: one line of signature, expand by qname.
type Neighbor struct {
	QName     string `json:"qname"`
	Signature string `json:"signature"`
	File      string `json:"file"`
	Line      int    `json:"line"`
	Depth     int    `json:"depth"`
}

// SymbolResponse answers a symbol-anchored query.
type SymbolResponse struct {
	Repo      string      `json:"repo"`
	Freshness Freshness   `json:"freshness"`
	Symbol    *SymbolInfo `json:"symbol,omitempty"`
	Callers   []Neighbor  `json:"callers,omitempty"`
	Callees   []Neighbor  `json:"callees,omitempty"`
	Path      []Neighbor  `json:"path,omitempty"`
	Matches   []Neighbor  `json:"matches,omitempty"`
	Truncated bool        `json:"truncated,omitempty"`
	// CallersTotal/CalleesTotal report the true neighbor count when the list was
	// truncated at maxNeighbors (issue #49) — depth=1 only (a multi-level total
	// means walking the whole closure, defeating the cap). Plain int, not a
	// pointer: set only on truncation, so the value is always ≥ maxNeighbors+1
	// and 0 (omitted) unambiguously means "not truncated".
	CallersTotal int `json:"callers_total,omitempty"`
	CalleesTotal int `json:"callees_total,omitempty"`
	// CallersInTests/CallersViaInterface are response-level splits over the
	// symbol's DIRECT (depth=1) callers, computed in SQL, independent of
	// maxNeighbors and of the request's own Depth (issue #48, #49, decision
	// 7a/2 amended). No per-row test/dispatch field exists on Neighbor — only
	// these response-level scalars.
	CallersInTests      int `json:"callers_in_tests,omitempty"`
	CallersViaInterface int `json:"callers_via_interface,omitempty"`
	// Carried reports whether the queried symbol's package rode on
	// carried-forward edges in the most recent build (its package is in the
	// build's carried_units set) — a partial-index honesty signal distinct
	// from Freshness.Stale (which a successful partial build does not set).
	Carried bool `json:"carried,omitempty"`
	// Precision names the queried symbol's own tier: "resolved" (Go,
	// type-checked) or "syntactic" (mapper: FactCalls + name/containment
	// heuristics, never type-checked). Because the two edge subgraphs are
	// disjoint (D.7 — TestEdges_NeverJoinGoSymbolToMapperSymbol), this one
	// value also describes every caller/callee counted below; there is no
	// separate per-caller-class breakdown (design D7, spec "Mixed
	// transitive_callers Count with Precision Label"). Set whenever Symbol
	// is non-nil; absent on a search-menu/ambiguous response, where there is
	// no single symbol whose tier could be named.
	Precision string `json:"precision,omitempty"`
	// TransitiveCallers is the blast size: distinct symbols that transitively
	// call this one (up-closure, capped). A pointer so 0 ("safe to change,
	// nothing calls it") is distinct from absent (a search-menu response, no
	// single symbol to score). Set only on an exact match.
	TransitiveCallers *int `json:"transitive_callers,omitempty"`
	// Implementers lists the concrete types satisfying this interface (issue
	// #48) — set only when the symbol is an interface. ImplementersTotal is the
	// true count even when Implementers is capped, and a pointer so 0 ("nobody
	// implements it — a dead interface, safe to change") is definitive, distinct
	// from absent ("not an interface").
	Implementers      []Neighbor `json:"implementers,omitempty"`
	ImplementersTotal *int       `json:"implementers_total,omitempty"`
	// Satisfies lists the repo-defined interfaces a concrete type implements.
	// Absent means it satisfies none (no *_total: a concrete type satisfies few
	// interfaces, never near the neighbor cap, so the list is its own count).
	Satisfies []Neighbor `json:"satisfies,omitempty"`
	Hint      string     `json:"hint,omitempty"`
}

// addHint appends extra to the "; "-joined hint chain h, empty-safe.
func addHint(h, extra string) string {
	if h == "" {
		return extra
	}
	return h + "; " + extra
}

// mapperFileExtensions is the syntactic (mapper) tier's extension set,
// mirroring mapperLanguages (mapper.go) but keyed by extension instead of
// grammar name — the only lookup Precision derivation needs. Any extension
// outside this set (in practice, only ".go") is the resolved tier.
var mapperFileExtensions = map[string]bool{
	".ts": true, ".tsx": true, ".js": true, ".jsx": true, ".mjs": true, ".py": true,
}

// symbolPrecision derives a symbol's precision class from its OWN file
// extension — zero extra SQL (design D7). Because the two edge subgraphs are
// disjoint (D.7, pinned by TestEdges_NeverJoinGoSymbolToMapperSymbol), a
// symbol's entire transitive closure lives in its own tier, so the queried
// symbol's own extension is enough to label the whole answer; no per-edge or
// per-caller precision read is needed.
func symbolPrecision(file string) string {
	if mapperFileExtensions[filepath.Ext(file)] {
		return precisionSyntactic
	}
	return precisionResolved
}

// weakestPrecision returns the weakest precision present in precisions
// ("syntactic" beats "resolved" — spec "Mixed transitive_callers Count with
// Precision Label"). Never called with genuinely mixed input in production:
// tier disjointness means a real symbol's callers are always single-tier, so
// Symbol() uses the cheap symbolPrecision(file) shortcut instead. This
// function pins the general "weakest wins" semantic that shortcut relies on,
// and is the documented fallback (D7's guard note) if the disjointness
// invariant is ever disproved: replace the shortcut call site with a real
// per-edge `SELECT precision FROM edges WHERE ...` fed through this same
// function. An empty/all-resolved input returns "resolved".
func weakestPrecision(precisions []string) string {
	for _, p := range precisions {
		if p == precisionSyntactic {
			return precisionSyntactic
		}
	}
	return precisionResolved
}

// Symbol resolves and answers a symbol-anchored query against repo's graph.
func (m *Manager) Symbol(ctx context.Context, req SymbolRequest) (*SymbolResponse, error) {
	if strings.TrimSpace(req.Repo) == "" {
		return nil, fmt.Errorf("repo is required: %w", ErrInvalidArgument)
	}
	if strings.TrimSpace(req.Symbol) == "" {
		return nil, fmt.Errorf("symbol is required: %w", ErrInvalidArgument)
	}
	conn, release, fresh, err := m.ensureFresh(ctx, req.Repo)
	if err != nil {
		return nil, err
	}
	defer release() // hold the handle for every statement below, not just the first
	m.bump(req.Repo, "symbol")
	resp := &SymbolResponse{Repo: req.Repo, Freshness: fresh, Hint: expandHint}
	if fresh.Stale {
		resp.Hint = staleGraphHint + "; " + expandHint
	}

	rows, err := findSymbol(ctx, conn, req.Symbol)
	if err != nil {
		return nil, err
	}
	switch {
	case len(rows) == 0:
		// No name resolved — treat the input as a task phrase and hand back a
		// BM25-ranked menu of relevant symbols (graph_symbol as search entry).
		seeds, err := searchSymbols(ctx, conn, req.Symbol)
		if err != nil {
			return nil, err
		}
		if len(seeds) == 0 {
			return nil, fmt.Errorf("symbol %q: %w", req.Symbol, ErrNotFound)
		}
		resp.Matches = seeds
		resp.Hint = searchHint
		return resp, nil
	case len(rows) > 1:
		resp.Matches = rows
		resp.Hint = ambiguousHint
		return resp, nil
	}

	var info SymbolInfo
	var id int64
	err = conn.QueryRowContext(ctx, `SELECT id, qname, kind, package, file, line, signature, doc, source
		FROM symbols WHERE qname = ?`, rows[0].QName).Scan(
		&id, &info.QName, &info.Kind, &info.Package, &info.File, &info.Line,
		&info.Signature, &info.Doc, &info.Source)
	if err != nil {
		return nil, err
	}
	resp.Symbol = &info
	resp.Carried = slices.Contains(fresh.carriedUnits, info.Package)
	resp.Precision = symbolPrecision(info.File)

	var blastHint string // see blastTypeHint/blastRefHint above for the why
	switch info.Kind {
	case "func", "method":
		tc, err := transitiveCallers(ctx, conn, id)
		if err != nil {
			return nil, err
		}
		resp.TransitiveCallers = &tc
	case "interface":
		// implementers ARE the interface's blast radius — the exact, not
		// CHA-approximate, set a method-signature change must update (issue #48).
		impls, total, trunc, err := implementers(ctx, conn, id)
		if err != nil {
			return nil, err
		}
		resp.Implementers = impls
		resp.ImplementersTotal = &total
		resp.Truncated = resp.Truncated || trunc
		blastHint = implementersHint
	case "type":
		// A concrete type: list the interfaces it satisfies, then redirect the
		// (call-edge) blast question to its methods — or to reference-level when
		// it has none, which has no call-graph handle at all (issue #47).
		sat, trunc, err := satisfies(ctx, conn, id)
		if err != nil {
			return nil, err
		}
		resp.Satisfies = sat
		resp.Truncated = resp.Truncated || trunc
		hasMethods, err := typeHasMethods(ctx, conn, info.QName)
		if err != nil {
			return nil, err
		}
		if hasMethods {
			blastHint = blastTypeHint
		} else {
			blastHint = blastRefHint
		}
	case "const", "var":
		blastHint = blastRefHint
	default:
		// Unknown future kind: assert nothing about its blast semantics — leave
		// the generic neighbor hint rather than guess. Revisit if a kind is added.
	}
	if blastHint != "" {
		resp.Hint = blastHint
		if fresh.Stale {
			resp.Hint = staleGraphHint + "; " + blastHint
		}
	}
	// Append, never replace: blastHint above assigns, so a carried symbol must
	// keep its blast-radius redirect as well as the carried caveat.
	if resp.Carried {
		resp.Hint = addHint(resp.Hint, carriedHint)
	}
	if fresh.Rebuilding {
		resp.Hint = addHint(resp.Hint, rebuildingHint)
	}
	if resp.Precision == precisionSyntactic {
		resp.Hint = addHint(resp.Hint, syntacticHint)
	}

	depth := min(max(req.Depth, 1), maxDepth)
	dir := req.Direction
	if dir == "" {
		dir = "both"
	}

	if req.To != "" {
		targets, err := findSymbol(ctx, conn, req.To)
		if err != nil {
			return nil, err
		}
		if len(targets) == 0 {
			return nil, fmt.Errorf("path target %q: %w", req.To, ErrNotFound)
		}
		if len(targets) > 1 {
			return nil, fmt.Errorf("path target %q is ambiguous (%d matches) — use an exact qname", req.To, len(targets))
		}
		path, err := callPath(ctx, conn, id, targets[0].QName)
		if err != nil {
			return nil, err
		}
		resp.Path = path
		return resp, nil
	}

	var upTrunc, downTrunc bool
	var callersTotal int // true depth=1 caller total, from callerSplit
	if dir == "up" || dir == "both" {
		resp.Callers, upTrunc, err = bfsNeighbors(ctx, conn, id, "up", depth, info.Package)
		if err != nil {
			return nil, err
		}
		callersTotal, resp.CallersInTests, resp.CallersViaInterface, err = callerSplit(ctx, conn, id)
		if err != nil {
			return nil, err
		}
	}
	if dir == "down" || dir == "both" {
		resp.Callees, downTrunc, err = bfsNeighbors(ctx, conn, id, "down", depth, info.Package)
		if err != nil {
			return nil, err
		}
	}
	// True neighbor totals only when a list was capped, and only at depth=1 (a
	// deeper total = full-closure walk, defeating the cap). See issue #49.
	// The caller total is already in hand: upTrunc implies the up branch ran,
	// so callerSplit counted exactly the same distinct callers.
	if upTrunc && depth == 1 {
		resp.CallersTotal = callersTotal
	}
	if downTrunc && depth == 1 {
		if resp.CalleesTotal, err = calleeCount(ctx, conn, id); err != nil {
			return nil, err
		}
	}
	if upTrunc || downTrunc {
		resp.Truncated = true
		resp.Hint = addHint(resp.Hint, truncatedHint)
	}
	return resp, nil
}

// calleeCount is the true neighbor total behind a truncated depth=1 callee
// list. The caller side needs no equivalent: callerSplit already returns it.
func calleeCount(ctx context.Context, conn *sql.DB, id int64) (int, error) {
	var n int
	err := conn.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT callee) FROM edges WHERE caller = ?`, id).Scan(&n)
	return n, err
}

// callerSplit computes response-level caller-fidelity splits over id's
// DIRECT (depth=1) callers in one pass over the edges table — total distinct
// callers, how many are in _test.go files, and how many are reached only via
// interface dispatch. All three are independent of maxNeighbors (issue #49)
// and of the request's own Depth (design.md: "all depth=1 only, computed in
// SQL"). The escaped LIKE avoids misclassifying a literal underscore (e.g.
// "helpertest.go") as a wildcard match (issue #48/decision 7a).
func callerSplit(ctx context.Context, conn *sql.DB, id int64) (total, inTests, viaInterface int, err error) {
	err = conn.QueryRowContext(ctx, `SELECT
			COUNT(DISTINCT e.caller),
			COUNT(DISTINCT CASE WHEN s.file LIKE '%\_test.go' ESCAPE '\' THEN e.caller END),
			COUNT(DISTINCT CASE WHEN e.dispatch = 'interface' THEN e.caller END)
		FROM edges e JOIN symbols s ON s.id = e.caller
		WHERE e.callee = ?`, id).Scan(&total, &inTests, &viaInterface)
	return total, inTests, viaInterface, err
}

// findSymbol resolves a name to symbol stubs: exact qname first, then short
// name, then qname suffix (e.g. "Store.Save" or "store.Save").
//
// The suffix rung tries both separators the two tiers use: the Go tier joins
// with '.' ("internal/store.Store.Save"), the mapper with ':' (modulePath +
// ":" + container + "." + name, mapper_symbols.go). A dot-anchored LIKE alone
// therefore never matches the receiver-qualified form the MCP schema
// documents, on any mapper language — and missing here does not surface as an
// error: the caller drops to the BM25 search fallback and answers with a menu
// instead of the symbol's body, callers, and callees.
func findSymbol(ctx context.Context, conn *sql.DB, name string) ([]Neighbor, error) {
	suffix := strings.TrimPrefix(name, ".")
	queries := []struct {
		where string
		arg   string
	}{
		{"qname = ?", name},
		{"name = ?", name},
		{"qname LIKE ?", "%." + suffix},
		{"qname LIKE ?", "%:" + suffix},
	}
	for _, q := range queries {
		rows, err := conn.QueryContext(ctx, `SELECT qname, signature, file, line FROM symbols
			WHERE `+q.where+` ORDER BY qname LIMIT ?`, q.arg, maxMatches+1) // #nosec G202 -- where clauses are compile-time constants above
		if err != nil {
			return nil, err
		}
		out, err := scanNeighbors(rows, 0)
		if err != nil {
			return nil, err
		}
		if len(out) > 0 {
			if len(out) > maxMatches {
				out = out[:maxMatches]
			}
			return out, nil
		}
	}
	return nil, nil
}

// searchSymbols ranks symbols by BM25 relevance to a free-text task phrase —
// the fallback when a name doesn't resolve, turning graph_symbol into a search
// entry: give it a task, get a ranked signature menu, drill the best by qname.
func searchSymbols(ctx context.Context, conn *sql.DB, task string) ([]Neighbor, error) {
	q := ftsQuery(task)
	if q == "" {
		return nil, nil
	}
	rows, err := conn.QueryContext(ctx, `SELECT s.qname, s.signature, s.file, s.line
		FROM symbols_fts f JOIN symbols s ON s.id = f.rowid
		WHERE symbols_fts MATCH ? ORDER BY bm25(symbols_fts) LIMIT ?`, q, maxSeeds)
	if err != nil {
		return nil, err
	}
	return scanNeighbors(rows, 0)
}

// ftsQuery turns a task phrase into a safe FTS5 OR-of-terms, dropping 1-char
// noise and quoting each token so punctuation can't inject MATCH syntax.
func ftsQuery(s string) string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	})
	terms := make([]string, 0, len(fields))
	for _, f := range fields {
		if len(f) < 2 {
			continue
		}
		terms = append(terms, `"`+f+`"`)
	}
	return strings.Join(terms, " OR ")
}

// transitiveCallers counts distinct symbols that transitively call id (up-
// closure), cycle-safe (UNION dedupes), and bounded by blastCap so a hot
// symbol on a big graph can't churn — the outer LIMIT stops the lazy CTE early.
func transitiveCallers(ctx context.Context, conn *sql.DB, id int64) (int, error) {
	var n int
	err := conn.QueryRowContext(ctx, `
		WITH RECURSIVE up(sym) AS (
			SELECT caller FROM edges WHERE callee = ?
			UNION
			SELECT e.caller FROM edges e JOIN up ON e.callee = up.sym
		)
		SELECT COUNT(*) FROM (SELECT sym FROM up LIMIT ?)`, id, blastCap).Scan(&n)
	return n, err
}

// typeHasMethods reports whether the type named by qname has any indexed
// methods, so a blast-radius query on it can point at them (a method's qname is
// the type's qname + "." + method). A method-less type has no call-graph handle.
// ponytail: LIKE prefix left unescaped — a '_' in the qname is a LIKE wildcard,
// but a false match only swaps in the method-redirect hint (the agent finds no
// methods and self-corrects), never a wrong answer, so no ESCAPE clause.
func typeHasMethods(ctx context.Context, conn *sql.DB, qname string) (bool, error) {
	var exists int
	err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM symbols
		WHERE kind = 'method' AND qname LIKE ?)`, qname+".%").Scan(&exists)
	return exists == 1, err
}

// implementers lists concrete types satisfying interface id, capped at
// maxNeighbors, plus the true total so a god-interface's capped list still
// reports its real size (AXI §4 — spares the agent a re-count call).
func implementers(ctx context.Context, conn *sql.DB, id int64) (rows []Neighbor, total int, truncated bool, err error) {
	if err = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM implements WHERE iface = ?`, id).Scan(&total); err != nil {
		return nil, 0, false, err
	}
	r, err := conn.QueryContext(ctx, `SELECT s.qname, s.signature, s.file, s.line
		FROM implements i JOIN symbols s ON s.id = i.impl
		WHERE i.iface = ? ORDER BY s.qname LIMIT ?`, id, maxNeighbors+1)
	if err != nil {
		return nil, 0, false, err
	}
	rows, err = scanNeighbors(r, 0)
	if err != nil {
		return nil, 0, false, err
	}
	if len(rows) > maxNeighbors {
		rows = rows[:maxNeighbors]
		truncated = true
	}
	return rows, total, truncated, nil
}

// satisfies lists the repo-defined interfaces concrete type id implements
// (reverse of implementers, served by idx_implements_impl). Capped at
// maxNeighbors with a truncation flag; no total (a type satisfies few
// interfaces, never near the cap, so the shown list is its own count).
func satisfies(ctx context.Context, conn *sql.DB, id int64) (rows []Neighbor, truncated bool, err error) {
	r, err := conn.QueryContext(ctx, `SELECT s.qname, s.signature, s.file, s.line
		FROM implements i JOIN symbols s ON s.id = i.iface
		WHERE i.impl = ? ORDER BY s.qname LIMIT ?`, id, maxNeighbors+1)
	if err != nil {
		return nil, false, err
	}
	rows, err = scanNeighbors(r, 0)
	if err != nil {
		return nil, false, err
	}
	if len(rows) > maxNeighbors {
		rows = rows[:maxNeighbors]
		truncated = true
	}
	return rows, truncated, nil
}

func scanNeighbors(rows *sql.Rows, depth int) ([]Neighbor, error) {
	defer rows.Close()
	var out []Neighbor
	for rows.Next() {
		var n Neighbor
		if err := rows.Scan(&n.QName, &n.Signature, &n.File, &n.Line); err != nil {
			return nil, err
		}
		n.Depth = depth
		out = append(out, n)
	}
	return out, rows.Err()
}

// bfsNeighbors walks call edges breadth-first up to depth, returning
// signature stubs, capped at maxNeighbors (truncated=true past the cap).
// startPkg biases the within-level ordering so same-package neighbors survive
// the cap first (issue #49) — a partial slice is less arbitrary that way.
func bfsNeighbors(ctx context.Context, conn *sql.DB, start int64, dir string, depth int, startPkg string) ([]Neighbor, bool, error) {
	from, to := "callee", "caller" // up: who calls me
	if dir == "down" {
		from, to = "caller", "callee"
	}
	seen := map[int64]bool{start: true}
	frontier := []int64{start}
	var out []Neighbor
	for d := 1; d <= depth && len(frontier) > 0; d++ {
		next, truncated, err := neighborLevel(ctx, conn, from, to, frontier, seen, &out, d, startPkg)
		if err != nil {
			return nil, false, err
		}
		if truncated {
			return out, true, nil
		}
		frontier = next
	}
	return out, false, nil
}

// neighborLevel expands one BFS level, appending stubs to out. truncated is
// true once out hits maxNeighbors.
func neighborLevel(ctx context.Context, conn *sql.DB, from, to string, frontier []int64, seen map[int64]bool,
	out *[]Neighbor, depth int, startPkg string) (next []int64, truncated bool, err error) {

	// ORDER BY is_test first: a same-package _test.go caller must NOT outrank a
	// cross-package production caller, or the cap can show zero production
	// callers and an agent wrongly concludes a signature change is test-only.
	// (s.package != ?) then puts same-package neighbors first within each
	// is_test group (0 < 1), so the cap keeps the closest, then qname. The
	// escaped LIKE avoids misclassifying a literal underscore (e.g.
	// "helpertest.go") as a wildcard match. The startPkg arg trails the
	// frontier IN placeholders — positional order must match (issue #49).
	rows, err := conn.QueryContext(ctx, fmt.Sprintf(`SELECT DISTINCT s.id, s.qname, s.signature, s.file, s.line
		FROM edges e JOIN symbols s ON s.id = e.%s
		WHERE e.%s IN (%s)
		ORDER BY (s.file LIKE '%%\_test.go' ESCAPE '\'), (s.package != ?), s.qname`, to, from, placeholders(len(frontier))),
		append(idArgs(frontier), startPkg)...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var n Neighbor
		if err := rows.Scan(&id, &n.QName, &n.Signature, &n.File, &n.Line); err != nil {
			return nil, false, err
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		n.Depth = depth
		if len(*out) >= maxNeighbors {
			return nil, true, nil
		}
		*out = append(*out, n)
		next = append(next, id)
	}
	return next, false, rows.Err()
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func idArgs(ids []int64) []any {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return args
}

// callPath BFSes caller→callee edges from start to the symbol named target,
// returning the shortest call chain including both endpoints.
func callPath(ctx context.Context, conn *sql.DB, start int64, targetQName string) ([]Neighbor, error) {
	var target int64
	if err := conn.QueryRowContext(ctx, `SELECT id FROM symbols WHERE qname = ?`, targetQName).Scan(&target); err != nil {
		return nil, err
	}
	parent := map[int64]int64{start: start}
	frontier := []int64{start}
	found := start == target
	for d := 0; d < maxPathDepth && len(frontier) > 0 && !found; d++ {
		next, err := pathLevel(ctx, conn, frontier, parent)
		if err != nil {
			return nil, err
		}
		frontier = next
		_, found = parent[target]
	}
	if !found {
		return nil, fmt.Errorf("no call path to %q within %d hops: %w", targetQName, maxPathDepth, ErrNotFound)
	}
	var ids []int64
	for at := target; ; at = parent[at] {
		ids = append([]int64{at}, ids...)
		if at == parent[at] {
			break
		}
	}
	out := make([]Neighbor, 0, len(ids))
	for i, id := range ids {
		var n Neighbor
		if err := conn.QueryRowContext(ctx, `SELECT qname, signature, file, line FROM symbols
			WHERE id = ?`, id).Scan(&n.QName, &n.Signature, &n.File, &n.Line); err != nil {
			return nil, err
		}
		n.Depth = i
		out = append(out, n)
	}
	return out, nil
}

// pathLevel expands one BFS level over caller→callee edges, recording each
// newly reached node's parent for path reconstruction.
func pathLevel(ctx context.Context, conn *sql.DB, frontier []int64, parent map[int64]int64) (next []int64, err error) {
	rows, err := conn.QueryContext(ctx, fmt.Sprintf(`SELECT caller, callee FROM edges
		WHERE caller IN (%s)`, placeholders(len(frontier))), idArgs(frontier)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c, t int64
		if err := rows.Scan(&c, &t); err != nil {
			return nil, err
		}
		if _, ok := parent[t]; ok {
			continue
		}
		parent[t] = c
		next = append(next, t)
	}
	return next, rows.Err()
}

// PackageRequest is a scope-anchored query (ADR-0020 tool 2): the public
// surface of one package.
type PackageRequest struct {
	Repo    string
	Package string // package path or suffix, e.g. "internal/store"
}

// PackageSymbol is one exported symbol's stub in a package surface.
type PackageSymbol struct {
	QName     string `json:"qname"`
	Kind      string `json:"kind"`
	Signature string `json:"signature"`
	Doc       string `json:"doc,omitempty"`
	File      string `json:"file"`
	Line      int    `json:"line"`
}

// PackageResponse answers a scope-anchored query.
type PackageResponse struct {
	Repo       string          `json:"repo"`
	Freshness  Freshness       `json:"freshness"`
	Package    string          `json:"package"`
	Symbols    []PackageSymbol `json:"symbols"`
	Unexported int             `json:"unexported_count"`
	Truncated  bool            `json:"truncated,omitempty"`
	Hint       string          `json:"hint,omitempty"`
}

// resolvePackage maps a user-supplied package name onto a real `package`
// value, accepting both shapes the MCP schema documents ("internal/store" or
// just "store"). The Go tier stores slash-separated paths but the mapper
// stores Python modules dotted, so a slash-anchored suffix alone answers
// not_found for every documented form on a Python repo except the exact dotted
// path. Two rungs, most literal first: the name as typed, then with '/'
// rewritten to '.'. Shortest match wins per rung — the old tie-break.
func resolvePackage(ctx context.Context, conn *sql.DB, name string) (string, error) {
	const q = `SELECT package FROM symbols
		WHERE package = ? OR package LIKE ? OR package LIKE ?
		ORDER BY length(package) LIMIT 1`

	pkg := strings.Trim(strings.TrimSpace(name), "/")
	if pkg == "" {
		return "", fmt.Errorf("package %q: %w", name, ErrInvalidArgument)
	}
	for _, cand := range []string{pkg, strings.ReplaceAll(pkg, "/", ".")} {
		if cand == "" {
			continue
		}
		var resolved string
		switch err := conn.QueryRowContext(ctx, q, cand, "%/"+cand, "%."+cand).Scan(&resolved); {
		case err == nil:
			return resolved, nil
		case !errors.Is(err, sql.ErrNoRows):
			return "", err
		}
	}
	return "", fmt.Errorf("package %q: %w", name, ErrNotFound)
}

// packageDirWhere builds a WHERE clause that matches directory-level packages
// for JS/TS (slash) and Python (dot) mapper modules. A query for a directory
// like "app/components/ui" has no symbol with package exactly that string —
// children live at "app/components/ui/button", "app/components/ui/input", etc.
// The clause matches:
//
//	package = dir
//	package LIKE dir || '/%'          (prefix: dir is ancestor)
//	package LIKE '%/' || dir           (suffix: bare leaf)
//	package LIKE '%/' || dir || '/%'   (infix: qualified mid-path)
//
// plus the same three with '.' for Python. This lets "ui", "components/ui",
// and "app/components/ui" all find "app/components/ui/button".
func packageDirWhere(pkg string) (string, []any) {
	pkg = strings.Trim(strings.TrimSpace(pkg), "/")
	candidates := []string{pkg}
	if dotted := strings.ReplaceAll(pkg, "/", "."); dotted != pkg {
		candidates = append(candidates, dotted)
	}
	var usable []string
	for _, c := range candidates {
		if c != "" {
			usable = append(usable, c)
		}
	}
	if len(usable) == 0 {
		return "1=0", nil
	}
	var clauses []string
	var args []any
	for _, cand := range usable {
		clauses = append(clauses, "package = ?")
		args = append(args, cand)
		clauses = append(clauses, "package LIKE ? || '/%'")
		args = append(args, cand)
		clauses = append(clauses, "package LIKE '%/' || ?")
		args = append(args, cand)
		clauses = append(clauses, "package LIKE '%/' || ? || '/%'")
		args = append(args, cand)
		clauses = append(clauses, "package LIKE ? || '.%'")
		args = append(args, cand)
		clauses = append(clauses, "package LIKE '%.' || ?")
		args = append(args, cand)
		clauses = append(clauses, "package LIKE '%.' || ? || '.%'")
		args = append(args, cand)
	}
	return "(" + strings.Join(clauses, " OR ") + ")", args
}

// Package returns the exported surface of one package: signatures + first
// doc line per symbol, never bodies.
func (m *Manager) Package(ctx context.Context, req PackageRequest) (*PackageResponse, error) {
	if strings.TrimSpace(req.Repo) == "" {
		return nil, fmt.Errorf("repo is required: %w", ErrInvalidArgument)
	}
	if strings.TrimSpace(req.Package) == "" {
		return nil, fmt.Errorf("package is required: %w", ErrInvalidArgument)
	}
	conn, release, fresh, err := m.ensureFresh(ctx, req.Repo)
	if err != nil {
		return nil, err
	}
	defer release() // hold the handle for every statement below, not just the first
	m.bump(req.Repo, "package")

	// First tier: exact or suffix (preserves Go and Python leaf behavior).
	if resolved, err := resolvePackage(ctx, conn, req.Package); err == nil {
		resp := &PackageResponse{Repo: req.Repo, Freshness: fresh, Package: resolved, Hint: pkgSymbolsLimit}
		var hints []string
		if fresh.Stale {
			hints = append(hints, staleGraphHint)
		}
		if fresh.Rebuilding {
			hints = append(hints, rebuildingHint)
		}
		if len(hints) > 0 {
			hints = append(hints, pkgSymbolsLimit)
			resp.Hint = strings.Join(hints, "; ")
		}
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM symbols WHERE package = ? AND exported = 0`,
			resolved).Scan(&resp.Unexported); err != nil {
			return nil, err
		}
		rows, err := conn.QueryContext(ctx, `SELECT qname, kind, signature, doc, file, line FROM symbols
			WHERE package = ? AND exported = 1 ORDER BY file, line LIMIT ?`,
			resolved, maxPkgSymbols+1)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var s PackageSymbol
			var doc string
			if err := rows.Scan(&s.QName, &s.Kind, &s.Signature, &doc, &s.File, &s.Line); err != nil {
				return nil, err
			}
			s.Doc = firstLine(doc)
			resp.Symbols = append(resp.Symbols, s)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if len(resp.Symbols) > maxPkgSymbols {
			resp.Symbols = resp.Symbols[:maxPkgSymbols]
			resp.Truncated = true
		}
		return resp, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	// Second tier: directory-level prefix aggregation for JS/TS (and Python dirs).
	// No exact/suffix package exists — treat the query as a directory path and
	// aggregate symbols from all descendant modules.
	pkgNorm := strings.Trim(strings.TrimSpace(req.Package), "/")
	where, args := packageDirWhere(pkgNorm)
	var total int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM symbols WHERE `+where, args...).Scan(&total); err != nil { // #nosec G202 -- where is built from placeholder ORs, not user string concatenation
		return nil, err
	}
	if total == 0 {
		return nil, fmt.Errorf("package %q: %w", req.Package, ErrNotFound)
	}
	resp := &PackageResponse{Repo: req.Repo, Freshness: fresh, Package: pkgNorm, Hint: pkgSymbolsLimit}
	var hints []string
	if fresh.Stale {
		hints = append(hints, staleGraphHint)
	}
	if fresh.Rebuilding {
		hints = append(hints, rebuildingHint)
	}
	if len(hints) > 0 {
		hints = append(hints, pkgSymbolsLimit)
		resp.Hint = strings.Join(hints, "; ")
	}
	// Unexported count across the aggregated directory.
	uq := `SELECT COUNT(*) FROM symbols WHERE ` + where + ` AND exported = 0` // #nosec G202 -- see above
	if err := conn.QueryRowContext(ctx, uq, args...).Scan(&resp.Unexported); err != nil {
		return nil, err
	}
	sq := `SELECT qname, kind, signature, doc, file, line FROM symbols WHERE ` + where + ` AND exported = 1 ORDER BY file, line LIMIT ?` // #nosec G202 -- see above
	sargs := append(append([]any{}, args...), maxPkgSymbols+1)
	rows, err := conn.QueryContext(ctx, sq, sargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var s PackageSymbol
		var doc string
		if err := rows.Scan(&s.QName, &s.Kind, &s.Signature, &doc, &s.File, &s.Line); err != nil {
			return nil, err
		}
		s.Doc = firstLine(doc)
		resp.Symbols = append(resp.Symbols, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(resp.Symbols) > maxPkgSymbols {
		resp.Symbols = resp.Symbols[:maxPkgSymbols]
		resp.Truncated = true
	}
	return resp, nil
}
