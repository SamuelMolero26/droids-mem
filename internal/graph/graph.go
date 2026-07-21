// Package graph is the native code-graph subsystem (ADR-0020): a per-repo
// index of a Go codebase's symbols and call edges, queried by agents for
// surgical code context instead of file-by-file crawling.
//
// It shares nothing with the Memory data model — graph rows are derived from
// source and regenerated whenever the repo changes, so no scrub, dedupe, or
// retention applies. Storage is one graph.db per repo, centralized under
// <state dir>/graphs/<repo-hash>/, keyed by canonical repo path.
package graph

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE symbols (
  id        INTEGER PRIMARY KEY,
  qname     TEXT NOT NULL,
  name      TEXT NOT NULL,
  kind      TEXT NOT NULL,
  package   TEXT NOT NULL,
  file      TEXT NOT NULL,
  line      INTEGER NOT NULL,
  exported  INTEGER NOT NULL,
  signature TEXT NOT NULL,
  doc       TEXT NOT NULL DEFAULT '',
  source    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_symbols_qname   ON symbols(qname);
CREATE INDEX idx_symbols_name    ON symbols(name);
CREATE INDEX idx_symbols_package ON symbols(package);
CREATE TABLE edges (
  caller INTEGER NOT NULL,
  callee INTEGER NOT NULL,
  PRIMARY KEY (caller, callee)
) WITHOUT ROWID;
CREATE INDEX idx_edges_callee ON edges(callee);
-- Implements edges (issue #48): iface → concrete type it is satisfied by.
-- Exact (types.Implements), not CHA-approximate. Both endpoints are repo-local
-- symbols.id, mirroring edges. Reverse index serves the "what does X satisfy"
-- direction (satisfies) the same way idx_edges_callee serves callers.
CREATE TABLE implements (
  iface INTEGER NOT NULL,
  impl  INTEGER NOT NULL,
  PRIMARY KEY (iface, impl)
) WITHOUT ROWID;
CREATE INDEX idx_implements_impl ON implements(impl);
-- Ranks symbols by relevance to a free-text task phrase (the graph_symbol
-- search fallback). rowid == symbols.id, so a MATCH joins straight back.
-- Populated wholesale in writeGraphDB — the graph never updates in place, so
-- no sync triggers are needed (unlike mem.db).
CREATE VIRTUAL TABLE symbols_fts USING fts5(
  qname, name, doc, signature, tokenize='porter unicode61'
);
`

// ErrNotFound reports a symbol or package with no match in the graph.
var ErrNotFound = errors.New("not found")

// Freshness is attached to every response so the agent can tell a fresh graph
// from a stale one (ADR-0020: go/packages needs compiling code, so a mid-edit
// repo serves the last good graph, marked stale).
type Freshness struct {
	Stamp      string `json:"stamp"`
	IndexedAt  string `json:"indexed_at"`
	Stale      bool   `json:"stale,omitempty"`
	Rebuilding bool   `json:"rebuilding,omitempty"`
	IndexError string `json:"index_error,omitempty"`
}

// stampTTL controls how long a stamp() result is cached per repo. The stamp
// walk (~5-100 ms depending on repo size) runs on every query without the
// cache; the TTL covers burst queries in an active agent session (the common
// case). A cache hit serves the previous stamp, skipping the walk entirely,
// but delays detection of a file edit by at most stampTTL before the stamp
// comparison fires a rebuild. 2 seconds balances burst speed vs detection lag.
//
// Exported as a var (not const) so tests can disable it with stampTTL = 0.
var stampTTL = 2 * time.Second

type stampEntry struct {
	stamp   string
	expires time.Time
}

// buildState tracks one in-flight async rebuild for a repo.
type buildState struct {
	ctx    context.Context
	cancel context.CancelFunc
	stamp  string        // the stamp value this build targets
	done   chan struct{} // closed when build finishes (success or failure)
}

// Manager routes queries to per-repo graph databases, rebuilding a repo's
// graph when its staleness stamp no longer matches the working tree.
type Manager struct {
	base string // e.g. ~/.droids-mem/graphs

	mu         sync.Mutex
	locks      map[string]*sync.Mutex
	conns      map[string]*sql.DB     // open handle per repo db
	stampCache map[string]*stampEntry // key: canonical repo path

	buildsMu sync.Mutex
	builds   map[string]*buildState // key: canonical repo path

	// lastBuildErrors holds the most recent build error per repo so the
	// warm-serve path can surface it via Freshness.IndexError. Protected by
	// buildsMu. Cleared when a new build starts.
	lastBuildErrors map[string]string
}

// NewManager creates a Manager storing graphs under base.
func NewManager(base string) *Manager {
	return &Manager{
		base:            base,
		locks:           make(map[string]*sync.Mutex),
		conns:           make(map[string]*sql.DB),
		stampCache:      make(map[string]*stampEntry),
		builds:          make(map[string]*buildState),
		lastBuildErrors: make(map[string]string),
	}
}

// Close releases all cached database handles and cancels any in-flight async
// rebuilds. Build goroutines detect the cancelled context and exit.
func (m *Manager) Close() {
	m.mu.Lock()
	for _, c := range m.conns {
		_ = c.Close()
	}
	m.conns = make(map[string]*sql.DB)
	m.mu.Unlock()

	m.buildsMu.Lock()
	for _, bs := range m.builds {
		bs.cancel()
	}
	m.builds = make(map[string]*buildState)
	m.buildsMu.Unlock()
}

// buildAsync runs buildIndex in a background goroutine, then cleans up the
// build tracking state. On success, it closes the old connection so the next
// open() picks up the newly-renamed graph.db. On failure, the old graph stays
// in place (served as stale) and the error is recorded in lastBuildErrors so
// the warm-serve path can surface it via Freshness.IndexError. If the build was
// superseded by a newer one (stamp changed during the build), the result is
// discarded (ensureFresh already cancelled the old build's context).
func (m *Manager) buildAsync(ctx context.Context, repo, path, stamp string) {
	buildErr := buildIndex(ctx, repo, path, stamp)

	m.buildsMu.Lock()
	defer m.buildsMu.Unlock()

	bs, ok := m.builds[repo]
	if !ok || bs.stamp != stamp {
		// Superseded by a newer build — discard (ensureFresh already cancelled
		// the old context, so the ctx here is already dead)
		return
	}
	delete(m.builds, repo)
	close(bs.done)
	bs.cancel() // release the cancel func so it doesn't leak

	if buildErr != nil {
		m.lastBuildErrors[repo] = buildErr.Error()
		return // failed — old graph stays in place as stale
	}
	// Success: clear any prior error and close the old connection so the next
	// open() picks up the new .db file
	delete(m.lastBuildErrors, repo)
	m.closeConn(path)
}

func (m *Manager) repoLock(repo string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.locks[repo]
	if !ok {
		l = &sync.Mutex{}
		m.locks[repo] = l
	}
	return l
}

func canonicalRepo(repo string) (string, error) {
	abs, err := filepath.Abs(repo)
	if err != nil {
		return "", fmt.Errorf("resolve repo path: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("repo path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("repo path %q is not a directory", abs)
	}
	return abs, nil
}

// bump records one query against tool ("symbol"|"package") as a single byte
// appended to a per-repo side-file, so the file size is the call count — an
// adoption signal (issue #51) telling whether agents actually use the tools.
// It lives beside graph.db, not inside it: the counter is the only mutation on
// the read path, and keeping it out preserves graph.db's query_only invariant.
// Best-effort — a dropped byte never fails a query.
//
// ponytail: 1 byte/call, file size = count. ~1KB per 1k calls; truncate to reset.
// Read it with `wc -c <state dir>/graphs/*/queries.*` — no CLI, deliberately.
func (m *Manager) bump(repo, tool string) {
	canon, err := canonicalRepo(repo)
	if err != nil {
		return
	}
	path := filepath.Join(filepath.Dir(m.dbPath(canon)), "queries."+tool)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 -- path is state dir + repo hash + constant tool name, no user input
	if err != nil {
		return
	}
	_, _ = f.Write([]byte{'.'})
	_ = f.Close()
}

func (m *Manager) dbPath(repo string) string {
	h := sha256.Sum256([]byte(repo))
	return filepath.Join(m.base, hex.EncodeToString(h[:6]), "graph.db")
}

// ensureFresh returns an open handle on the repo's graph db, rebuilding it
// when the working tree changed since the stored stamp.
//
// First build (no graph exists yet): synchronous buildIndex. Error returns
// directly — there is no prior graph to fall back to.
//
// Warm-serve (stale graph exists): the rebuild launches asynchronously and the
// caller gets the stale graph back with Freshness.{Stale,Rebuilding} set. The
// next query either finds the fresh graph or continues warm-serving. A caller
// that wants to wait for the async build to finish can call WaitBuild.
func (m *Manager) ensureFresh(ctx context.Context, repo string) (*sql.DB, Freshness, error) {
	repo, err := canonicalRepo(repo)
	if err != nil {
		return nil, Freshness{}, err
	}
	lock := m.repoLock(repo)
	lock.Lock()

	current, err := m.cachedStamp(repo)
	if err != nil {
		lock.Unlock()
		return nil, Freshness{}, err
	}
	path := m.dbPath(repo)

	conn, fresh, err := m.open(path)
	if err == nil && fresh.Stamp == current {
		lock.Unlock()
		return conn, fresh, nil
	}

	// First build: sync (no previous graph to serve stale)
	if conn == nil {
		buildErr := buildIndex(ctx, repo, path, current)
		if buildErr != nil {
			lock.Unlock()
			return nil, Freshness{}, fmt.Errorf("index %s: %w", repo, buildErr)
		}
		m.closeConn(path)
		conn, fresh, err = m.open(path)
		if err != nil {
			lock.Unlock()
			return nil, Freshness{}, err
		}
		lock.Unlock()
		return conn, fresh, nil
	}

	// Warm-serve: graph exists but stamp mismatched
	m.buildsMu.Lock()
	lastErr := m.lastBuildErrors[repo]
	bs, building := m.builds[repo]
	if building && bs.stamp == current {
		// Same-stamp build already running — attach and return stale.
		// If the previous build failed, surface the error even while retrying.
		m.buildsMu.Unlock()
		fresh.Stale = true
		fresh.Rebuilding = true
		if lastErr != "" {
			fresh.IndexError = lastErr
		}
		lock.Unlock()
		return conn, fresh, nil
	}
	if building {
		// Stamp changed — cancel the in-flight build, it's already stale
		bs.cancel()
		delete(m.builds, repo)
	}

	// Launch new async build with background context
	bgCtx, cancel := context.WithCancel(context.Background())
	m.builds[repo] = &buildState{
		ctx:    bgCtx,
		cancel: cancel,
		stamp:  current,
		done:   make(chan struct{}),
	}
	// Surface the last build error while a new build is in-flight if one
	// exists — the agent deserves to know why the graph was stale before
	// the retry. The error is cleared when the new build finishes (success
	// deletes it; failure replaces it).
	fresh.IndexError = lastErr
	m.buildsMu.Unlock()
	lock.Unlock() // Release BEFORE goroutine — critical

	go m.buildAsync(bgCtx, repo, path, current)

	fresh.Stale = true
	fresh.Rebuilding = true
	return conn, fresh, nil
}

// WaitBuild blocks until any active async build for repo finishes,
// or until timeout. Returns whether the build completed (false = timeout
// or no build was in progress) and whether it succeeded.
func (m *Manager) WaitBuild(ctx context.Context, repo string, timeout time.Duration) (*BuildWaitResponse, error) {
	repo, err := canonicalRepo(repo)
	if err != nil {
		return nil, err
	}

	m.buildsMu.Lock()
	bs, ok := m.builds[repo]
	m.buildsMu.Unlock()

	if !ok {
		// No active build — check current freshness
		conn, fresh, err := m.ensureFresh(ctx, repo)
		if err != nil {
			return nil, err
		}
		_ = conn // just need freshness
		return &BuildWaitResponse{
			Repo:      repo,
			Completed: true,
			Rebuilt:   !fresh.Stale,
			Freshness: fresh,
		}, nil
	}

	// Wait with timeout
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	select {
	case <-bs.done:
		// Build finished — check result
		conn, fresh, err := m.ensureFresh(waitCtx, repo)
		if err != nil {
			return nil, err
		}
		_ = conn
		return &BuildWaitResponse{
			Repo:      repo,
			Completed: true,
			Rebuilt:   !fresh.Stale,
			Freshness: fresh,
		}, nil
	case <-waitCtx.Done():
		return &BuildWaitResponse{
			Repo:      repo,
			Completed: false,
			Freshness: Freshness{Stale: true, Rebuilding: true},
		}, nil
	}
}

// BuildWaitResponse reports the outcome of a WaitBuild call.
type BuildWaitResponse struct {
	Repo      string    `json:"repo"`
	Completed bool      `json:"completed"`         // false = timeout or no build
	Rebuilt   bool      `json:"rebuilt,omitempty"` // true = build succeeded, graph fresh
	Freshness Freshness `json:"freshness"`
}

// open returns the cached handle for path (opening it if needed) plus its
// stored freshness meta. A missing db is (nil, zero, nil-error from cache
// perspective): callers treat conn==nil as "no graph yet".
func (m *Manager) open(path string) (*sql.DB, Freshness, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, Freshness{}, nil // absent db is not an error: "no graph yet"
		}
		return nil, Freshness{}, fmt.Errorf("stat graph db: %w", err)
	}
	m.mu.Lock()
	conn, ok := m.conns[path]
	m.mu.Unlock()
	if !ok {
		var err error
		conn, err = sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=query_only(true)")
		if err != nil {
			return nil, Freshness{}, fmt.Errorf("open graph db: %w", err)
		}
		m.mu.Lock()
		m.conns[path] = conn
		m.mu.Unlock()
	}
	var fresh Freshness
	rows, err := conn.Query(`SELECT key, value FROM meta WHERE key IN ('stamp','indexed_at')`)
	if err != nil {
		return nil, Freshness{}, fmt.Errorf("read graph meta: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, Freshness{}, err
		}
		switch k {
		case "stamp":
			fresh.Stamp = v
		case "indexed_at":
			fresh.IndexedAt = v
		}
	}
	return conn, fresh, rows.Err()
}

func (m *Manager) closeConn(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.conns[path]; ok {
		_ = c.Close()
		delete(m.conns, path)
	}
}

// cachedStamp returns the stamp for repo, caching the result so rapid-fire
// queries during an agent session skip the file-tree walk. Returns a fresh
// stamp from stamp() when the cache is empty or expired (stampTTL).
func (m *Manager) cachedStamp(repo string) (string, error) {
	if stampTTL <= 0 {
		return stamp(repo)
	}
	m.mu.Lock()
	if e, ok := m.stampCache[repo]; ok && time.Now().Before(e.expires) {
		m.mu.Unlock()
		return e.stamp, nil
	}
	m.mu.Unlock()

	s, err := stamp(repo)
	if err != nil {
		return "", err
	}

	m.mu.Lock()
	m.stampCache[repo] = &stampEntry{stamp: s, expires: time.Now().Add(stampTTL)}
	m.mu.Unlock()
	return s, nil
}

// Index force-builds (or refreshes) the graph for repo and reports its size.
func (m *Manager) Index(ctx context.Context, repo string) (*IndexResponse, error) {
	repo, err := canonicalRepo(repo)
	if err != nil {
		return nil, err
	}
	lock := m.repoLock(repo)
	lock.Lock()
	defer lock.Unlock()

	current, err := m.cachedStamp(repo)
	if err != nil {
		return nil, err
	}
	path := m.dbPath(repo)
	if err := buildIndex(ctx, repo, path, current); err != nil {
		return nil, fmt.Errorf("index %s: %w", repo, err)
	}
	m.closeConn(path)
	conn, fresh, err := m.open(path)
	if err != nil {
		return nil, err
	}
	resp := &IndexResponse{Repo: repo, Freshness: fresh}
	if err := conn.QueryRow(`SELECT COUNT(*) FROM symbols`).Scan(&resp.Symbols); err != nil {
		return nil, err
	}
	if err := conn.QueryRow(`SELECT COUNT(*) FROM edges`).Scan(&resp.Edges); err != nil {
		return nil, err
	}
	return resp, nil
}

// IndexResponse reports the outcome of an explicit index build.
type IndexResponse struct {
	Repo      string    `json:"repo"`
	Symbols   int       `json:"symbols"`
	Edges     int       `json:"edges"`
	Freshness Freshness `json:"freshness"`
}

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }
