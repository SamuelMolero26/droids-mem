package graph

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

const (
	maxDepth        = 5
	maxPathDepth    = 10
	maxNeighbors    = 50 // per direction, across all depths
	maxMatches      = 20
	maxPkgSymbols   = 200
	expandHint      = "neighbors are signatures only; call graph_symbol with a neighbor's exact qname to see its body"
	ambiguousHint   = "multiple symbols share that name; re-query with one of the qnames in matches"
	searchHint      = "no exact symbol match; these are the closest by relevance — re-query graph_symbol with one qname from matches for its full body, callers, and callees"
	maxSeeds        = 10  // search-fallback menu size
	blastCap        = 500 // transitive-caller count sentinel (compute + number guard)
	staleGraphHint  = "graph is stale: the repo changed but no longer type-checks, serving the last good index"
	pkgSymbolsLimit = "exported symbols only; query an unexported symbol by name via graph_symbol"
)

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
	// TransitiveCallers is the blast size: distinct symbols that transitively
	// call this one (up-closure, capped). A pointer so 0 ("safe to change,
	// nothing calls it") is distinct from absent (a search-menu response, no
	// single symbol to score). Set only on an exact match.
	TransitiveCallers *int   `json:"transitive_callers,omitempty"`
	Hint              string `json:"hint,omitempty"`
}

// Symbol resolves and answers a symbol-anchored query against repo's graph.
func (m *Manager) Symbol(ctx context.Context, req SymbolRequest) (*SymbolResponse, error) {
	conn, fresh, err := m.ensureFresh(ctx, req.Repo)
	if err != nil {
		return nil, err
	}
	resp := &SymbolResponse{Repo: req.Repo, Freshness: fresh, Hint: expandHint}
	if fresh.Stale {
		resp.Hint = staleGraphHint + "; " + expandHint
	}

	rows, err := findSymbol(conn, req.Symbol)
	if err != nil {
		return nil, err
	}
	switch {
	case len(rows) == 0:
		// No name resolved — treat the input as a task phrase and hand back a
		// BM25-ranked menu of relevant symbols (graph_symbol as search entry).
		seeds, err := searchSymbols(conn, req.Symbol)
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
	err = conn.QueryRow(`SELECT id, qname, kind, package, file, line, signature, doc, source
		FROM symbols WHERE qname = ?`, rows[0].QName).Scan(
		&id, &info.QName, &info.Kind, &info.Package, &info.File, &info.Line,
		&info.Signature, &info.Doc, &info.Source)
	if err != nil {
		return nil, err
	}
	resp.Symbol = &info

	tc, err := transitiveCallers(conn, id)
	if err != nil {
		return nil, err
	}
	resp.TransitiveCallers = &tc

	depth := req.Depth
	if depth < 1 {
		depth = 1
	}
	if depth > maxDepth {
		depth = maxDepth
	}
	dir := req.Direction
	if dir == "" {
		dir = "both"
	}

	if req.To != "" {
		targets, err := findSymbol(conn, req.To)
		if err != nil {
			return nil, err
		}
		if len(targets) == 0 {
			return nil, fmt.Errorf("path target %q: %w", req.To, ErrNotFound)
		}
		if len(targets) > 1 {
			return nil, fmt.Errorf("path target %q is ambiguous (%d matches) — use an exact qname", req.To, len(targets))
		}
		path, err := callPath(conn, id, targets[0].QName)
		if err != nil {
			return nil, err
		}
		resp.Path = path
		return resp, nil
	}

	if dir == "up" || dir == "both" {
		resp.Callers, resp.Truncated, err = bfsNeighbors(conn, id, "up", depth)
		if err != nil {
			return nil, err
		}
	}
	if dir == "down" || dir == "both" {
		var trunc bool
		resp.Callees, trunc, err = bfsNeighbors(conn, id, "down", depth)
		if err != nil {
			return nil, err
		}
		resp.Truncated = resp.Truncated || trunc
	}
	return resp, nil
}

// findSymbol resolves a name to symbol stubs: exact qname first, then short
// name, then qname suffix (e.g. "Store.Save" or "store.Save").
func findSymbol(conn *sql.DB, name string) ([]Neighbor, error) {
	queries := []struct {
		where string
		arg   string
	}{
		{"qname = ?", name},
		{"name = ?", name},
		{"qname LIKE ?", "%." + strings.TrimPrefix(name, ".")},
	}
	for _, q := range queries {
		rows, err := conn.Query(`SELECT qname, signature, file, line FROM symbols
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
func searchSymbols(conn *sql.DB, task string) ([]Neighbor, error) {
	q := ftsQuery(task)
	if q == "" {
		return nil, nil
	}
	rows, err := conn.Query(`SELECT s.qname, s.signature, s.file, s.line
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
func transitiveCallers(conn *sql.DB, id int64) (int, error) {
	var n int
	err := conn.QueryRow(`
		WITH RECURSIVE up(sym) AS (
			SELECT caller FROM edges WHERE callee = ?
			UNION
			SELECT e.caller FROM edges e JOIN up ON e.callee = up.sym
		)
		SELECT COUNT(*) FROM (SELECT sym FROM up LIMIT ?)`, id, blastCap).Scan(&n)
	return n, err
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
func bfsNeighbors(conn *sql.DB, start int64, dir string, depth int) ([]Neighbor, bool, error) {
	from, to := "callee", "caller" // up: who calls me
	if dir == "down" {
		from, to = "caller", "callee"
	}
	seen := map[int64]bool{start: true}
	frontier := []int64{start}
	var out []Neighbor
	for d := 1; d <= depth && len(frontier) > 0; d++ {
		next, truncated, err := neighborLevel(conn, from, to, frontier, seen, &out, d)
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
func neighborLevel(conn *sql.DB, from, to string, frontier []int64, seen map[int64]bool,
	out *[]Neighbor, depth int) (next []int64, truncated bool, err error) {

	rows, err := conn.Query(fmt.Sprintf(`SELECT DISTINCT s.id, s.qname, s.signature, s.file, s.line
		FROM edges e JOIN symbols s ON s.id = e.%s
		WHERE e.%s IN (%s) ORDER BY s.qname`, to, from, placeholders(len(frontier))), idArgs(frontier)...)
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
func callPath(conn *sql.DB, start int64, targetQName string) ([]Neighbor, error) {
	var target int64
	if err := conn.QueryRow(`SELECT id FROM symbols WHERE qname = ?`, targetQName).Scan(&target); err != nil {
		return nil, err
	}
	parent := map[int64]int64{start: start}
	frontier := []int64{start}
	found := start == target
	for d := 0; d < maxPathDepth && len(frontier) > 0 && !found; d++ {
		next, err := pathLevel(conn, frontier, parent)
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
		if err := conn.QueryRow(`SELECT qname, signature, file, line FROM symbols
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
func pathLevel(conn *sql.DB, frontier []int64, parent map[int64]int64) (next []int64, err error) {
	rows, err := conn.Query(fmt.Sprintf(`SELECT caller, callee FROM edges
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

// Package returns the exported surface of one package: signatures + first
// doc line per symbol, never bodies.
func (m *Manager) Package(ctx context.Context, req PackageRequest) (*PackageResponse, error) {
	conn, fresh, err := m.ensureFresh(ctx, req.Repo)
	if err != nil {
		return nil, err
	}
	pkg := strings.Trim(req.Package, "/")
	var resolved string
	err = conn.QueryRow(`SELECT package FROM symbols
		WHERE package = ? OR package LIKE ? ORDER BY length(package) LIMIT 1`,
		pkg, "%/"+pkg).Scan(&resolved)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("package %q: %w", req.Package, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}

	resp := &PackageResponse{Repo: req.Repo, Freshness: fresh, Package: resolved, Hint: pkgSymbolsLimit}
	if fresh.Stale {
		resp.Hint = staleGraphHint + "; " + pkgSymbolsLimit
	}
	if err := conn.QueryRow(`SELECT COUNT(*) FROM symbols WHERE package = ? AND exported = 0`,
		resolved).Scan(&resp.Unexported); err != nil {
		return nil, err
	}
	rows, err := conn.Query(`SELECT qname, kind, signature, doc, file, line FROM symbols
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
}
