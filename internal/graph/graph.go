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
	IndexError string `json:"index_error,omitempty"`
}

// Manager routes queries to per-repo graph databases, rebuilding a repo's
// graph when its staleness stamp no longer matches the working tree.
type Manager struct {
	base string // e.g. ~/.droids-mem/graphs

	mu    sync.Mutex
	locks map[string]*sync.Mutex // per canonical repo path
	conns map[string]*sql.DB     // open handle per repo db
}

// NewManager creates a Manager storing graphs under base.
func NewManager(base string) *Manager {
	return &Manager{
		base:  base,
		locks: make(map[string]*sync.Mutex),
		conns: make(map[string]*sql.DB),
	}
}

// Close releases all cached database handles.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.conns {
		_ = c.Close()
	}
	m.conns = make(map[string]*sql.DB)
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

func (m *Manager) dbPath(repo string) string {
	h := sha256.Sum256([]byte(repo))
	return filepath.Join(m.base, hex.EncodeToString(h[:6]), "graph.db")
}

// ensureFresh returns an open handle on the repo's graph db, rebuilding it
// first when the working tree changed since the stored stamp. If a rebuild
// fails (repo does not type-check mid-edit) and a previous graph exists, that
// graph is served with Freshness.Stale set — honest degradation over failure.
func (m *Manager) ensureFresh(ctx context.Context, repo string) (*sql.DB, Freshness, error) {
	repo, err := canonicalRepo(repo)
	if err != nil {
		return nil, Freshness{}, err
	}
	lock := m.repoLock(repo)
	lock.Lock()
	defer lock.Unlock()

	current, err := stamp(repo)
	if err != nil {
		return nil, Freshness{}, err
	}
	path := m.dbPath(repo)

	conn, fresh, err := m.open(path)
	if err == nil && fresh.Stamp == current {
		return conn, fresh, nil
	}

	buildErr := buildIndex(ctx, repo, path, current)
	if buildErr != nil {
		if conn != nil { // stale graph beats no graph
			fresh.Stale = true
			fresh.IndexError = buildErr.Error()
			return conn, fresh, nil
		}
		return nil, Freshness{}, fmt.Errorf("index %s: %w", repo, buildErr)
	}

	m.closeConn(path)
	conn, fresh, err = m.open(path)
	if err != nil {
		return nil, Freshness{}, err
	}
	return conn, fresh, nil
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

// Index force-builds (or refreshes) the graph for repo and reports its size.
func (m *Manager) Index(ctx context.Context, repo string) (*IndexResponse, error) {
	repo, err := canonicalRepo(repo)
	if err != nil {
		return nil, err
	}
	lock := m.repoLock(repo)
	lock.Lock()
	defer lock.Unlock()

	current, err := stamp(repo)
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
