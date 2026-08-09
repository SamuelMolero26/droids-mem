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
//
// done is closed exactly once, by whoever retires the state: buildAsync when
// its own build finishes, or ensureFresh when it supersedes the build. A
// retired state is always removed from Manager.builds first, so the two paths
// can never both close the same channel.
type buildState struct {
	ctx    context.Context
	cancel context.CancelFunc
	stamp  string        // the stamp value this build targets
	done   chan struct{} // closed when build finishes, fails, or is superseded
}

// connEntry is a refcounted handle on one graph.db. A rebuild replaces the file
// by rename and then retires the old handle, but a query that already took the
// handle may still be running several statements against it — Symbol alone runs
// half a dozen. Closing it out from under that caller surfaced as
// "sql: database is closed" mid-response, so retirement is deferred until the
// last holder releases.
type connEntry struct {
	db     *sql.DB
	refs   int
	dead   bool // retired from the map; close once refs drops to zero
	closed bool
}

// retire marks e dead and closes it if nobody holds it. Caller holds Manager.mu.
func (e *connEntry) retire() {
	e.dead = true
	e.maybeClose()
}

// release drops one reference. Caller must NOT hold Manager.mu.
func (m *Manager) release(e *connEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e.refs--
	e.maybeClose()
}

// maybeClose closes the handle once it is retired and unreferenced. Caller
// holds Manager.mu.
func (e *connEntry) maybeClose() {
	if e.dead && e.refs <= 0 && !e.closed {
		e.closed = true
		_ = e.db.Close()
	}
}

// Manager routes queries to per-repo graph databases, rebuilding a repo's
// graph when its staleness stamp no longer matches the working tree.
type Manager struct {
	base string // e.g. ~/.droids-mem/graphs

	mu         sync.Mutex
	locks      map[string]chan struct{} // 1-buffered semaphore per repo; see repoLock
	conns      map[string]*connEntry    // refcounted handle per repo db
	stampCache map[string]*stampEntry   // key: canonical repo path

	buildsMu sync.Mutex
	builds   map[string]*buildState // key: canonical repo path

	// lastBuildErrors holds the most recent build error per repo so the
	// warm-serve path can surface it via Freshness.IndexError. Protected by
	// buildsMu. A successful build deletes the entry; a failing one overwrites
	// it. It is deliberately NOT cleared when a build starts: an agent querying
	// mid-rebuild deserves to know why the graph went stale in the first place.
	lastBuildErrors map[string]string

	// failedStamps records, per repo, the stamp whose build failed. While the
	// working tree still hashes to that stamp, rebuilding is pointless — it
	// would fail identically — so ensureFresh serves stale without relaunching.
	// Any edit moves the stamp and lifts the suppression on the next query.
	// Protected by buildsMu.
	failedStamps map[string]string
}

// NewManager creates a Manager storing graphs under base.
func NewManager(base string) *Manager {
	return &Manager{
		base:            base,
		locks:           make(map[string]chan struct{}),
		conns:           make(map[string]*connEntry),
		stampCache:      make(map[string]*stampEntry),
		builds:          make(map[string]*buildState),
		lastBuildErrors: make(map[string]string),
		failedStamps:    make(map[string]string),
	}
}

// Close releases all cached database handles and cancels any in-flight async
// rebuilds. Build goroutines detect the cancelled context and exit.
func (m *Manager) Close() {
	m.mu.Lock()
	for _, e := range m.conns {
		e.retire() // in-flight queries close theirs on release
	}
	m.conns = make(map[string]*connEntry)
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
func (m *Manager) buildAsync(bs *buildState, repo, path string) {
	buildErr := buildIndex(bs.ctx, repo, path, bs.stamp)

	m.buildsMu.Lock()
	defer m.buildsMu.Unlock()

	if m.builds[repo] != bs {
		// Superseded by a newer build — discard. ensureFresh already cancelled
		// this build's context AND closed its done channel when it retired the
		// state, so there is nothing left to clean up here. The comparison is on
		// identity, not stamp: a stamp can repeat, and matching one would let
		// this goroutine retire a live build it never ran.
		return
	}

	if buildErr != nil {
		// Failed — old graph stays in place as stale. Signal the waiter and
		// remember which source failed so ensureFresh stops relaunching an
		// identical doomed build on every subsequent query.
		m.lastBuildErrors[repo] = buildErr.Error()
		m.failedStamps[repo] = bs.stamp
		delete(m.builds, repo)
		close(bs.done)
		bs.cancel()
		return
	}
	// Success: clear any prior error and retire the old connection so the next
	// open() picks up the new .db file. Retiring MUST happen before close(done):
	// a WaitBuild caller that wakes on done re-reads state via open(), and a
	// still-cached old handle would report the pre-rename stamp — Rebuilt:false
	// for a build that actually succeeded (CI-flaky).
	delete(m.lastBuildErrors, repo)
	delete(m.failedStamps, repo)
	m.closeConn(path)
	delete(m.builds, repo)
	close(bs.done)
	bs.cancel() // release the cancel func so it doesn't leak
}

// finishColdBuild records the outcome of a first-ever index and publishes it.
// The cold path is not covered by buildAsync's bookkeeping, so without this a
// failure was forgotten (the next query paid for the same doomed build) and an
// abandoned success never retired the cached handle — which matters when the
// cold path was entered because an existing graph.db would not open: nothing
// would pick up the replacement and the repo re-entered the cold path forever.
func (m *Manager) finishColdBuild(repo, path, stamp string, err error) {
	m.buildsMu.Lock()
	if err != nil {
		m.lastBuildErrors[repo] = err.Error()
		m.failedStamps[repo] = stamp
		m.buildsMu.Unlock()
		return
	}
	delete(m.lastBuildErrors, repo)
	delete(m.failedStamps, repo)
	m.buildsMu.Unlock()
	m.closeConn(path)
}

// suppressedFailure reports whether stamp is the exact source that already
// failed to build, along with the recorded reason.
func (m *Manager) suppressedFailure(repo, stamp string) (bool, string) {
	m.buildsMu.Lock()
	defer m.buildsMu.Unlock()
	if m.failedStamps[repo] != stamp {
		return false, ""
	}
	return true, m.lastBuildErrors[repo]
}

// repoLock returns the per-repo build lock. It is a 1-buffered channel rather
// than a sync.Mutex because acquiring it must be abandonable: an abandoned cold
// build keeps the lock until it lands (deliberately — that is what stops a
// second caller starting a duplicate build), and a sync.Mutex would make every
// other caller wait out the whole build with its own deadline unenforceable.
func (m *Manager) repoLock(repo string) chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.locks[repo]
	if !ok {
		l = make(chan struct{}, 1)
		m.locks[repo] = l
	}
	return l
}

// acquireLock takes l, or gives up when ctx dies. Either the lock is held on a
// nil return, or it is not and the caller must not release it.
//
// The uncontended case is tried first and unconditionally: a plain two-way
// select picks randomly when the lock is free AND ctx is already dead, which
// would make an already-cancelled caller sometimes fail to start the cold build
// that is supposed to outlive it. Only a lock someone else holds is worth
// abandoning.
func acquireLock(ctx context.Context, l chan struct{}) error {
	select {
	case l <- struct{}{}:
		return nil
	default:
	}
	select {
	case l <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// releaseLock hands l back. Like the sync.Mutex it replaces, it may be called
// from a goroutine other than the acquirer — the abandoned-cold-build path does
// exactly that.
func releaseLock(l chan struct{}) { <-l }

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
// when the working tree changed since the stored stamp. The returned release
// func must be called when the caller is done with the handle — it is always
// non-nil, even when conn is nil.
//
// First build (no graph exists yet): synchronous buildIndex, using the caller's
// ctx — the caller is waiting on it, so the caller's deadline governs. Error
// returns directly; there is no prior graph to fall back to. An existing but
// unopenable graph.db lands here too (open reports conn==nil), which silently
// self-heals a corrupt file by rebuilding it.
//
// Warm-serve (stale graph exists): the rebuild launches asynchronously on a
// background context — it outlives the request that noticed the staleness — and
// the caller gets the stale graph back with Freshness.{Stale,Rebuilding} set.
// The next query either finds the fresh graph or continues warm-serving. A
// caller that wants to wait for the async build can call WaitBuild.
//
// A stamp whose build already failed is served stale WITHOUT relaunching: the
// same source would fail the same way, and a broken tree is exactly when an
// agent queries most.
func (m *Manager) ensureFresh(ctx context.Context, repo string) (*sql.DB, func(), Freshness, error) {
	repo, err := canonicalRepo(repo)
	if err != nil {
		return nil, noopRelease, Freshness{}, err
	}
	lock := m.repoLock(repo)
	if err := acquireLock(ctx, lock); err != nil {
		return nil, noopRelease, Freshness{}, err
	}

	current, err := m.cachedStamp(repo)
	if err != nil {
		releaseLock(lock)
		return nil, noopRelease, Freshness{}, err
	}
	path := m.dbPath(repo)

	conn, release, fresh, err := m.open(path)
	if err == nil && fresh.Stamp == current {
		releaseLock(lock)
		return conn, release, fresh, nil
	}

	// First build: the caller waits, because there is no prior graph to serve
	// stale. Who waits and what the build runs on are separate concerns:
	//
	//   - the build runs on WithoutCancel, so a caller that goes away mid-index
	//     (client disconnect, agent cancel, WaitBuild timeout) does not discard
	//     it. Abandoning it wrote nothing, so the next query restarted from zero
	//     and a repo whose clients kept disconnecting could never finish indexing.
	//   - the caller's wait is bounded by its own ctx, so a deadline still means
	//     something — WaitBuild's timeout in particular.
	//
	// On abandonment the build keeps repoLock until it lands, so the next caller
	// blocks and then finds a finished graph rather than starting a second build.
	if conn == nil {
		release()
		if failed, why := m.suppressedFailure(repo, current); failed {
			// Same source already failed to index, and there is no stale graph to
			// fall back on. Report the recorded reason instead of paying for an
			// identical doomed build on every query.
			releaseLock(lock)
			return nil, noopRelease, Freshness{}, fmt.Errorf("index %s: %s", repo, why)
		}
		done := make(chan error, 1) // buffered: the build never blocks on a gone caller
		go func() { done <- buildIndex(context.WithoutCancel(ctx), repo, path, current) }()

		var buildErr error
		select {
		case buildErr = <-done:
		case <-ctx.Done():
			// Hand lock ownership to the build: it still has to record its
			// outcome, and the next caller must find a finished graph rather
			// than start a second one.
			go func() {
				m.finishColdBuild(repo, path, current, <-done)
				releaseLock(lock)
			}()
			return nil, noopRelease, Freshness{}, ctx.Err()
		}
		m.finishColdBuild(repo, path, current, buildErr)
		if buildErr != nil {
			releaseLock(lock)
			return nil, noopRelease, Freshness{}, fmt.Errorf("index %s: %w", repo, buildErr)
		}
		conn, release, fresh, err = m.open(path)
		if err != nil {
			releaseLock(lock)
			return nil, noopRelease, Freshness{}, err
		}
		releaseLock(lock)
		return conn, release, fresh, nil
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
		fresh.IndexError = lastErr
		releaseLock(lock)
		return conn, release, fresh, nil
	}
	if building {
		// Stamp changed — the in-flight build targets dead source. Cancel it,
		// drop it, and close its done channel: a WaitBuild caller blocked on
		// this state must not wait out its full timeout for a build whose
		// result is already discarded. This runs before the failed-stamp check
		// below so a suppressed rebuild still retires the doomed build.
		bs.cancel()
		delete(m.builds, repo)
		close(bs.done)
	}
	if m.failedStamps[repo] == current {
		// This exact source already failed to build. Serve stale with the
		// reason and launch nothing — retrying burns a full go/packages load
		// per query for a guaranteed-identical failure.
		m.buildsMu.Unlock()
		fresh.Stale = true
		fresh.IndexError = lastErr
		releaseLock(lock)
		return conn, release, fresh, nil
	}

	// Launch new async build with background context
	bgCtx, cancel := context.WithCancel(context.Background())
	launched := &buildState{
		ctx:    bgCtx,
		cancel: cancel,
		stamp:  current,
		done:   make(chan struct{}),
	}
	m.builds[repo] = launched
	// Surface the last build error while a new build is in-flight if one
	// exists — the agent deserves to know why the graph was stale before
	// the retry. The error is cleared when the new build finishes (success
	// deletes it; failure replaces it).
	fresh.IndexError = lastErr
	m.buildsMu.Unlock()
	releaseLock(lock) // Release BEFORE goroutine — critical

	go m.buildAsync(launched, repo, path)

	fresh.Stale = true
	fresh.Rebuilding = true
	return conn, release, fresh, nil
}

// WaitBuild brings repo's graph up to date and blocks until that work
// finishes, or until timeout — whichever comes first. Completed reports whether
// the graph settled within the timeout; Rebuilt whether it is now fresh.
//
// The timeout governs every path, including the synchronous cold build of a
// never-indexed repo. An expired timeout is reported as Completed:false, not as
// an error: the caller asked to wait a bounded time, and running out of it is a
// normal answer. Errors are reserved for a repo that cannot be indexed at all.
//
// Waiting never launches a *second* build. A rebuild already in flight is
// attached to; one that is needed but unstarted is triggered exactly once via
// ensureFresh and then awaited.
func (m *Manager) WaitBuild(ctx context.Context, repo string, timeout time.Duration) (*BuildWaitResponse, error) {
	repo, err := canonicalRepo(repo)
	if err != nil {
		return nil, err
	}

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Trigger whatever is needed: cold builds run synchronously here (bounded
	// by waitCtx), stale ones launch an async rebuild we then wait on.
	_, release, fresh, err := m.ensureFresh(waitCtx, repo)
	if err != nil {
		if waitCtx.Err() != nil && ctx.Err() == nil {
			// The timeout expired mid-cold-build, not a real indexing failure.
			return m.waitTimedOut(repo), nil
		}
		return nil, err
	}
	release()

	if !fresh.Stale {
		return &BuildWaitResponse{Repo: repo, Completed: true, Rebuilt: true, Freshness: fresh}, nil
	}

	m.buildsMu.Lock()
	bs, ok := m.builds[repo]
	m.buildsMu.Unlock()
	if !ok {
		// Stale with nothing in flight: the build already finished (or is
		// suppressed as a known failure). Re-read rather than reusing the
		// snapshot from before the trigger — do not relaunch, or a broken repo
		// would rebuild on every wait.
		settled, ferr := m.freshnessNow(repo)
		if ferr != nil {
			return nil, ferr
		}
		return &BuildWaitResponse{Repo: repo, Completed: true, Rebuilt: !settled.Stale, Freshness: settled}, nil
	}

	select {
	case <-bs.done:
		// Build finished (or was superseded). Read the resulting state without
		// launching anything new.
		settled, ferr := m.freshnessNow(repo)
		if ferr != nil {
			return nil, ferr
		}
		return &BuildWaitResponse{
			Repo:      repo,
			Completed: true,
			Rebuilt:   !settled.Stale,
			Freshness: settled,
		}, nil
	case <-waitCtx.Done():
		return m.waitTimedOut(repo), nil
	}
}

// waitTimedOut reports a timeout against the graph currently on disk, so the
// caller still sees the real stamp of what is being served rather than a
// fabricated empty Freshness.
func (m *Manager) waitTimedOut(repo string) *BuildWaitResponse {
	fresh, err := m.freshnessNow(repo)
	if err != nil {
		fresh = Freshness{}
	}
	fresh.Stale = true
	fresh.Rebuilding = true
	return &BuildWaitResponse{Repo: repo, Completed: false, Freshness: fresh}
}

// freshnessNow reports repo's current freshness without ever launching a build.
// It is the read-only counterpart to ensureFresh, used by WaitBuild after a
// build settles — calling ensureFresh there would start yet another rebuild
// whenever the one we just awaited had failed.
func (m *Manager) freshnessNow(repo string) (Freshness, error) {
	current, err := m.cachedStamp(repo)
	if err != nil {
		return Freshness{}, err
	}
	_, release, fresh, err := m.open(m.dbPath(repo))
	if err != nil {
		return Freshness{}, err
	}
	defer release()

	m.buildsMu.Lock()
	fresh.IndexError = m.lastBuildErrors[repo]
	_, building := m.builds[repo]
	m.buildsMu.Unlock()

	fresh.Stale = fresh.Stamp != current
	fresh.Rebuilding = building
	return fresh, nil
}

// BuildWaitResponse reports the outcome of a WaitBuild call.
type BuildWaitResponse struct {
	Repo      string    `json:"repo"`
	Completed bool      `json:"completed"`         // false = timeout or no build
	Rebuilt   bool      `json:"rebuilt,omitempty"` // true = build succeeded, graph fresh
	Freshness Freshness `json:"freshness"`
}

// noopRelease is returned alongside a nil handle so every caller can
// `defer release()` unconditionally.
func noopRelease() {}

// open returns the cached handle for path (opening it if needed), a release
// func the caller must invoke when done with the handle, and the stored
// freshness meta. A missing db is (nil, noop, zero, nil-error from cache
// perspective): callers treat conn==nil as "no graph yet".
//
// The returned handle is refcounted: a rebuild landing mid-query retires the
// entry but cannot close it until this release runs.
func (m *Manager) open(path string) (*sql.DB, func(), Freshness, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, noopRelease, Freshness{}, nil // absent db is not an error: "no graph yet"
		}
		return nil, noopRelease, Freshness{}, fmt.Errorf("stat graph db: %w", err)
	}
	m.mu.Lock()
	entry, ok := m.conns[path]
	if !ok {
		db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=query_only(true)")
		if err != nil {
			m.mu.Unlock()
			return nil, noopRelease, Freshness{}, fmt.Errorf("open graph db: %w", err)
		}
		entry = &connEntry{db: db}
		m.conns[path] = entry
	}
	entry.refs++ // held until the returned release runs
	m.mu.Unlock()

	release := func() { m.release(entry) }

	var fresh Freshness
	rows, err := entry.db.Query(`SELECT key, value FROM meta WHERE key IN ('stamp','indexed_at')`)
	if err != nil {
		release()
		return nil, noopRelease, Freshness{}, fmt.Errorf("read graph meta: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			release()
			return nil, noopRelease, Freshness{}, err
		}
		switch k {
		case "stamp":
			fresh.Stamp = v
		case "indexed_at":
			fresh.IndexedAt = v
		}
	}
	if err := rows.Err(); err != nil {
		release()
		return nil, noopRelease, Freshness{}, err
	}
	return entry.db, release, fresh, nil
}

// closeConn retires the cached handle for path so the next open() picks up a
// newly renamed graph.db. The underlying *sql.DB is closed here only if nobody
// holds it; otherwise the last release closes it.
func (m *Manager) closeConn(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.conns[path]; ok {
		e.retire()
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
	if err := acquireLock(ctx, lock); err != nil {
		return nil, err
	}
	defer releaseLock(lock)

	current, err := m.cachedStamp(repo)
	if err != nil {
		return nil, err
	}
	path := m.dbPath(repo)
	if err := buildIndex(ctx, repo, path, current); err != nil {
		return nil, fmt.Errorf("index %s: %w", repo, err)
	}
	// A forced build that succeeded clears whatever any earlier failure recorded
	// and retires the old handle so the next open() sees the new file.
	m.finishColdBuild(repo, path, current, nil)
	conn, release, fresh, err := m.open(path)
	if err != nil {
		return nil, err
	}
	defer release()
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
