package graph

// Read-only browse API for the graph viewer UI. Every method here answers from
// the graph already on disk and never builds or rebuilds one: a viewer request
// must not trigger a go/packages load, and the graph is built by the CLI
// (`droids-mem graph ui`), possibly in another process.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	maxOverviewPkgs  = 400
	maxOverviewEdges = 2000
	maxBrowseSymbols = 500
	maxSearchResults = 30
	maxEntryPoints   = 100
	minSearchLen     = 2
	entryPointsHint  = "heuristic: exported or main/init/route functions with no non-test callers; dynamic dispatch, reflection and framework wiring are invisible to the call graph, so this is a starting point, not the full list of entry points"
	notIndexedHint   = "not indexed: run `droids-mem graph ui` in the repository to build the graph"
	testFileOf       = `%s.file LIKE '%%\_test.go' ESCAPE '\'`
)

// RepoRoot resolves dir to the repository scope a graph is keyed by, and
// rejects a directory that is not inside a Go module or git checkout (the
// walk silently returns a bare directory unchanged).
func RepoRoot(dir string) (string, error) {
	root, err := canonicalRepo(dir)
	if err != nil {
		return "", err
	}
	for _, anchor := range []string{"go.mod", ".git"} {
		if _, err := os.Stat(filepath.Join(root, anchor)); err == nil {
			return root, nil
		}
	}
	return "", fmt.Errorf("%s is not inside a repository (no go.mod or .git above it): %w", dir, ErrInvalidArgument)
}

// openFor picks the handle policy: no-build for read-only viewers, the
// build-on-demand path for everything else.
func (m *Manager) openFor(ctx context.Context, repo string, noBuild bool) (*sql.DB, func(), Freshness, error) {
	if noBuild {
		return m.openNoBuild(ctx, repo)
	}
	return m.ensureFresh(ctx, repo)
}

// openNoBuild opens the repo's existing graph without ever building. It
// returns ErrNotFound when no graph this binary can read exists.
func (m *Manager) openNoBuild(ctx context.Context, repo string) (*sql.DB, func(), Freshness, error) {
	repo, err := canonicalRepo(repo)
	if err != nil {
		return nil, noopRelease, Freshness{}, err
	}
	current, err := m.cachedStamp(repo)
	if err != nil {
		return nil, noopRelease, Freshness{}, err
	}
	path := m.dbPath(repo)
	notIndexed := fmt.Errorf("%s: %w", notIndexedHint, ErrNotFound)

	// The graph is usually built by another process (the CLI), which replaces
	// graph.db by rename: a handle cached here still points at the old inode.
	// Compare against the file itself and reopen when they diverge.
	onDisk := stampOnDisk(ctx, path)
	if onDisk == "" {
		return nil, noopRelease, Freshness{}, notIndexed
	}
	conn, release, fresh, err := m.open(path)
	if err != nil {
		return nil, noopRelease, Freshness{}, err
	}
	if conn != nil && fresh.Stamp != onDisk {
		release()
		m.closeConn(path)
		conn, release, fresh, err = m.open(path)
		if err != nil {
			return nil, noopRelease, Freshness{}, err
		}
	}
	// A graph from another generation cannot be read by this binary's queries.
	if conn == nil || !strings.HasPrefix(fresh.Stamp, currentGen+":") {
		release()
		return nil, noopRelease, Freshness{}, notIndexed
	}

	m.buildsMu.Lock()
	fresh.IndexError = m.lastBuildErrors[repo]
	_, fresh.Rebuilding = m.builds[repo]
	m.buildsMu.Unlock()
	fresh.Stale = fresh.Stamp != current
	return conn, release, fresh, nil
}

// PkgNode is one package in the overview map.
type PkgNode struct {
	Name      string `json:"name"`
	Symbols   int    `json:"symbols"`
	Precision string `json:"precision"`
	Carried   bool   `json:"carried,omitempty"`
}

// PkgEdge is the number of call edges from package From into package To.
type PkgEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Calls int    `json:"calls"`
}

// OverviewResponse is the package-level map of one repo.
type OverviewResponse struct {
	Repo      string    `json:"repo"`
	Freshness Freshness `json:"freshness"`
	Packages  []PkgNode `json:"packages"`
	Edges     []PkgEdge `json:"edges"`
	Truncated bool      `json:"truncated,omitempty"`
}

// PackageOverview returns packages and the cross-package call counts between
// them, test files excluded. Packages are kept by symbol count and edges by
// call count when a cap is hit.
func (m *Manager) PackageOverview(ctx context.Context, repo string) (*OverviewResponse, error) {
	conn, release, fresh, err := m.openNoBuild(ctx, repo)
	if err != nil {
		return nil, err
	}
	defer release()
	resp := &OverviewResponse{Repo: repo, Freshness: fresh, Packages: []PkgNode{}, Edges: []PkgEdge{}}

	rows, err := conn.QueryContext(ctx, `SELECT package, COUNT(*), COUNT(CASE WHEN file LIKE '%.go' THEN 1 END)
		FROM symbols WHERE NOT `+isTestFile+` GROUP BY package
		ORDER BY COUNT(*) DESC, package LIMIT ?`, maxOverviewPkgs+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	kept := map[string]bool{}
	for rows.Next() {
		var n PkgNode
		var goRows int
		if err := rows.Scan(&n.Name, &n.Symbols, &goRows); err != nil {
			return nil, err
		}
		if len(resp.Packages) == maxOverviewPkgs {
			resp.Truncated = true
			break
		}
		n.Precision = precisionResolved
		if goRows == 0 { // tiers are disjoint per package, so no .go rows means the mapper
			n.Precision = precisionSyntactic
		}
		n.Carried = slices.Contains(fresh.carriedUnits, n.Name)
		kept[n.Name] = true
		resp.Packages = append(resp.Packages, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	erows, err := conn.QueryContext(ctx, `SELECT cs.package, ce.package, COUNT(*)
		FROM edges e JOIN symbols cs ON cs.id = e.caller JOIN symbols ce ON ce.id = e.callee
		WHERE cs.package <> ce.package AND NOT `+fmt.Sprintf(testFileOf, "cs")+` AND NOT `+fmt.Sprintf(testFileOf, "ce")+`
		GROUP BY cs.package, ce.package ORDER BY COUNT(*) DESC, cs.package, ce.package LIMIT ?`, maxOverviewEdges+1) // #nosec G202 -- fragments are compile-time constants
	if err != nil {
		return nil, err
	}
	defer erows.Close()
	for erows.Next() {
		var e PkgEdge
		if err := erows.Scan(&e.From, &e.To, &e.Calls); err != nil {
			return nil, err
		}
		if len(resp.Edges) == maxOverviewEdges {
			resp.Truncated = true
			break
		}
		if kept[e.From] && kept[e.To] {
			resp.Edges = append(resp.Edges, e)
		}
	}
	return resp, erows.Err()
}

// BrowseSymbol is a symbol stub in a package listing; unlike PackageSymbol it
// also covers unexported symbols.
type BrowseSymbol struct {
	QName     string `json:"qname"`
	Kind      string `json:"kind"`
	Signature string `json:"signature"`
	File      string `json:"file"`
	Line      int    `json:"line"`
	Exported  bool   `json:"exported"`
}

// PackageSymbolsResponse lists every non-test symbol of one package.
type PackageSymbolsResponse struct {
	Repo      string         `json:"repo"`
	Freshness Freshness      `json:"freshness"`
	Package   string         `json:"package"`
	Symbols   []BrowseSymbol `json:"symbols"`
	Total     int            `json:"total,omitempty"` // true count, set only on truncation
	Truncated bool           `json:"truncated,omitempty"`
}

// PackageSymbols lists the symbols of the package named exactly pkg (the name
// PackageOverview returned), capped at maxBrowseSymbols.
func (m *Manager) PackageSymbols(ctx context.Context, repo, pkg string) (*PackageSymbolsResponse, error) {
	if strings.TrimSpace(pkg) == "" {
		return nil, fmt.Errorf("package is required: %w", ErrInvalidArgument)
	}
	conn, release, fresh, err := m.openNoBuild(ctx, repo)
	if err != nil {
		return nil, err
	}
	defer release()
	resp := &PackageSymbolsResponse{Repo: repo, Freshness: fresh, Package: pkg, Symbols: []BrowseSymbol{}}

	rows, err := conn.QueryContext(ctx, `SELECT qname, kind, signature, file, line, exported FROM symbols
		WHERE package = ? AND NOT `+isTestFile+` ORDER BY file, line LIMIT ?`, pkg, maxBrowseSymbols+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var s BrowseSymbol
		if err := rows.Scan(&s.QName, &s.Kind, &s.Signature, &s.File, &s.Line, &s.Exported); err != nil {
			return nil, err
		}
		resp.Symbols = append(resp.Symbols, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(resp.Symbols) == 0 {
		return nil, fmt.Errorf("package %q: %w", pkg, ErrNotFound)
	}
	if len(resp.Symbols) > maxBrowseSymbols {
		resp.Symbols = resp.Symbols[:maxBrowseSymbols]
		resp.Truncated = true
		err = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM symbols WHERE package = ? AND NOT `+isTestFile, pkg).Scan(&resp.Total)
	}
	return resp, err
}

// StubsResponse is a capped list of symbol stubs (search results, entry points).
type StubsResponse struct {
	Repo      string     `json:"repo"`
	Freshness Freshness  `json:"freshness"`
	Symbols   []Neighbor `json:"symbols"`
	Truncated bool       `json:"truncated,omitempty"`
	Hint      string     `json:"hint,omitempty"`
}

// SearchSymbols matches q as a name (or method name) prefix first, then by FTS relevance, capped
// at maxSearchResults. Queries shorter than minSearchLen are rejected.
func (m *Manager) SearchSymbols(ctx context.Context, repo, q string) (*StubsResponse, error) {
	q = strings.TrimSpace(q)
	if len([]rune(q)) < minSearchLen {
		return nil, fmt.Errorf("query must be at least %d characters: %w", minSearchLen, ErrInvalidArgument)
	}
	conn, release, fresh, err := m.openNoBuild(ctx, repo)
	if err != nil {
		return nil, err
	}
	defer release()
	resp := &StubsResponse{Repo: repo, Freshness: fresh, Symbols: []Neighbor{}}

	seen := map[string]bool{}
	add := func(query string, args ...any) error {
		rows, err := conn.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		got, err := scanNeighbors(rows, 0)
		if err != nil {
			return err
		}
		for _, n := range got {
			if !seen[n.QName] && len(resp.Symbols) < maxSearchResults {
				seen[n.QName] = true
				resp.Symbols = append(resp.Symbols, n)
			}
		}
		return nil
	}

	if err := add(`SELECT qname, signature, file, line FROM symbols
		WHERE name LIKE ? ESCAPE '\' OR name LIKE ? ESCAPE '\' ORDER BY length(name), qname LIMIT ?`,
		escapeLike(q)+"%", "%."+escapeLike(q)+"%", maxSearchResults); err != nil {
		return nil, err
	}
	if fq := ftsQuery(q); fq != "" && len(resp.Symbols) < maxSearchResults {
		if err := add(`SELECT s.qname, s.signature, s.file, s.line
			FROM symbols_fts f JOIN symbols s ON s.id = f.rowid
			WHERE symbols_fts MATCH ? ORDER BY bm25(symbols_fts) LIMIT ?`, fq, maxSearchResults); err != nil {
			return nil, err
		}
	}
	return resp, nil
}

// EntryPoints lists functions and methods that nothing outside tests calls and
// that look like roots (main/init, route-named, or exported). Heuristic: the
// call graph cannot see dynamic dispatch, so Hint says so.
func (m *Manager) EntryPoints(ctx context.Context, repo string) (*StubsResponse, error) {
	conn, release, fresh, err := m.openNoBuild(ctx, repo)
	if err != nil {
		return nil, err
	}
	defer release()
	resp := &StubsResponse{Repo: repo, Freshness: fresh, Symbols: []Neighbor{}, Hint: entryPointsHint}

	rows, err := conn.QueryContext(ctx, `SELECT s.qname, s.signature, s.file, s.line FROM symbols s
		WHERE s.kind IN ('func', 'method') AND NOT `+fmt.Sprintf(testFileOf, "s")+`
		AND (s.name IN ('main', 'init') OR s.exported = 1 OR lower(s.name) LIKE '%route%')
		AND NOT EXISTS (SELECT 1 FROM edges e JOIN symbols c ON c.id = e.caller
			WHERE e.callee = s.id AND NOT `+fmt.Sprintf(testFileOf, "c")+`)
		ORDER BY (s.name = 'main') DESC, s.package, s.file, s.line LIMIT ?`, maxEntryPoints+1) // #nosec G202 -- fragments are compile-time constants
	if err != nil {
		return nil, err
	}
	if resp.Symbols, err = scanNeighbors(rows, 0); err != nil {
		return nil, err
	}
	if resp.Symbols == nil {
		resp.Symbols = []Neighbor{}
	}
	if len(resp.Symbols) > maxEntryPoints {
		resp.Symbols = resp.Symbols[:maxEntryPoints]
		resp.Truncated = true
	}
	return resp, nil
}
