package store_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/samuelmolero26/droids-mem/internal/store"
)

// Recall benchmark (ADR-0025): measures mem_search / mem_context retrieval on a
// fixed corpus queried by paraphrases. Clusters share vocabulary so a query must
// rank its target above near-neighbours. synonym_hard queries share no surface
// tokens with their target. Deterministic; runs in CI.
//
//	go test ./internal/store -run TestRecallBenchmark -v
//	EVAL_WRITE_REPORT=1 go test ...   # rewrites eval/RESULTS.md
//
// Without EVAL_WRITE_REPORT the committed eval/RESULTS.md is asserted as a
// golden, so ranking or corpus changes cannot land with a stale report.

// benchCorpus is the fixed benchmark corpus. Clusters share vocabulary so a
// query must rank its target above near-neighbours.
var benchCorpus = []store.SaveRequest{
	// git
	{TaskType: "git", Kind: "error_resolution", Title: "Stash pop conflicts mean a wrong branch base, not a content merge",
		What: "git stash pop produced conflicts where every upstream side was empty", Learned: "when a popped stash conflicts and all additions are on the stashed side, the branch you popped onto is older than where the work was written; fix the base, do not hand-resolve", Tags: "git stash rebase base"},
	{TaskType: "git", Kind: "task_pattern", Title: "Rebase onto main instead of merging to keep history linear",
		What: "feature branches accumulated merge commits from main", Learned: "rebase the feature branch onto main so history stays linear and bisect works", Tags: "git rebase history linear"},
	{TaskType: "git", Kind: "error_resolution", Title: "Recover a force-pushed branch with the reflog",
		What: "a teammate force-pushed and overwrote commits", Learned: "the overwritten commits survive in the reflog; git reset --hard to the lost SHA restores them", Tags: "git reflog force-push recovery"},
	{TaskType: "git", Kind: "error_resolution", Title: "Detached HEAD after checking out a tag drops commits on the floor",
		What: "committed while in detached HEAD state after checking out a tag", Learned: "create a branch before committing on a detached HEAD or the commits become unreachable", Tags: "git detached-head tag branch"},

	// ── concurrency ─────────────────────────────────────────────────────────
	{TaskType: "concurrency", Kind: "error_resolution", Title: "Goroutine leak from sending on a channel with no receiver",
		What: "goroutines blocked forever writing to a channel nothing read", Learned: "an unbuffered send with no receiver blocks the goroutine forever; use a buffered channel or a select with a done channel to let it exit", Tags: "goroutine channel leak select"},
	{TaskType: "concurrency", Kind: "error_resolution", Title: "Data race on a shared map fixed with an RWMutex",
		What: "concurrent reads and writes to one map triggered the race detector", Learned: "guard a map shared across goroutines with sync.RWMutex, or the runtime corrupts it", Tags: "race map rwmutex sync"},
	{TaskType: "concurrency", Kind: "error_resolution", Title: "Deadlock from acquiring two mutexes in different orders",
		What: "two goroutines each held one lock and waited for the other", Learned: "always acquire multiple mutexes in a single global order to prevent the circular wait", Tags: "deadlock mutex lock-order"},
	{TaskType: "concurrency", Kind: "task_pattern", Title: "Propagate context cancellation into every blocking call",
		What: "a cancelled request kept downstream work running", Learned: "thread ctx into every blocking call so cancellation actually stops the work instead of leaking it", Tags: "context cancellation propagation"},

	// ── runtime-panics ──────────────────────────────────────────────────────
	{TaskType: "runtime-panics", Kind: "error_resolution", Title: "Nil pointer dereference on an unchecked map lookup",
		What: "read a struct field off a map value that was absent", Learned: "a missing key returns the zero value; check the comma-ok bool before dereferencing or you panic on a nil pointer", Tags: "nil pointer map panic"},
	{TaskType: "runtime-panics", Kind: "error_resolution", Title: "Nil interface is not nil: the typed-nil comparison trap",
		What: "returned a nil *T as an error interface and the == nil check failed", Learned: "an interface holding a typed nil is itself non-nil; return a bare nil, never a typed nil pointer, from an interface-returning function", Tags: "interface typed-nil comparison"},
	{TaskType: "runtime-panics", Kind: "error_resolution", Title: "Slice bounds out of range from an off-by-one loop",
		What: "indexed one past the end of a slice in a loop", Learned: "loop conditions using <= on len(s) run one index too far; use < len(s)", Tags: "slice bounds off-by-one loop"},

	// ── http-clients ────────────────────────────────────────────────────────
	{TaskType: "http-clients", Kind: "task_pattern", Title: "Back off and retry on HTTP 429 with jitter",
		What: "hammering an API on 429 got the client rate-limited harder", Learned: "on a 429, wait an exponentially growing delay with random jitter before retrying, and cap the number of attempts", Tags: "http 429 retry backoff jitter"},
	{TaskType: "http-clients", Kind: "error_resolution", Title: "HTTP client hangs forever without a request timeout",
		What: "a request to a slow host never returned", Learned: "the default http.Client has no timeout; set one so a dead upstream cannot block the caller indefinitely", Tags: "http client timeout deadline"},
	{TaskType: "http-clients", Kind: "error_resolution", Title: "JSON unmarshal silently zero-values mismatched types",
		What: "a number sent as a string decoded to zero with no error", Learned: "encoding/json leaves a field at its zero value on a type mismatch without erroring; validate after decode or use json.Number", Tags: "json unmarshal type-mismatch"},

	// ── performance ─────────────────────────────────────────────────────────
	{TaskType: "performance", Kind: "error_resolution", Title: "N+1 queries fixed by batching with an IN clause",
		What: "a loop issued one query per row", Learned: "collect the ids and fetch them in a single query with an IN clause instead of one round trip per row", Tags: "n+1 query batch in-clause"},
	{TaskType: "performance", Kind: "error_resolution", Title: "Missing index turns a lookup into a full table scan",
		What: "a WHERE on an unindexed column scanned every row", Learned: "add an index on the filtered column so the planner does a seek instead of a full table scan", Tags: "index full-scan query-plan"},
	{TaskType: "performance", Kind: "task_pattern", Title: "Reuse a buffer to kill allocation in a hot loop",
		What: "allocating inside a hot loop thrashed the garbage collector", Learned: "hoist the allocation out of the loop and reuse one buffer, or pool it, to cut GC pressure on the hot path", Tags: "allocation hot-loop gc buffer"},

	// ── ci ──────────────────────────────────────────────────────────────────
	{TaskType: "ci", Kind: "error_resolution", Title: "Local-green but CI-red: the base merge introduces a semantic conflict",
		What: "the same SHA passed locally but failed in CI", Learned: "pull_request CI tests the merge of your branch into its base, not your branch; a clean auto-merge can still be a semantic conflict, reproduce with git merge --no-commit against the base", Tags: "ci pull-request merge semantic-conflict"},
	{TaskType: "ci", Kind: "error_resolution", Title: "Flaky test from relying on map iteration order",
		What: "a test passed or failed depending on run", Learned: "Go randomizes map iteration order; sort the keys before asserting or the test is non-deterministic", Tags: "flaky test map-order determinism"},
	{TaskType: "ci", Kind: "error_resolution", Title: "go mod tidy drift fails CI: commit the tidied go.sum",
		What: "CI failed on a go.mod/go.sum diff", Learned: "run go mod tidy and commit the result; CI runs it and fails on any diff", Tags: "go-mod tidy go-sum ci"},

	// ── droids-mem (dogfood) ─────────────────────────────────────────────────
	{TaskType: "droids-mem", Kind: "task_pattern", Title: "Close the paraphrase retrieval gap with a porter stemmer, no embeddings",
		What: "BM25 missed morphological variants of saved lessons", Learned: "switch the FTS5 tokenizer to porter unicode61 so cancel/cancellation and panic/panicked collapse to one stem; keeps it local-first and pure-Go, no embeddings, no CGO", Tags: "porter stemmer fts5 paraphrase"},
	{TaskType: "droids-mem", Kind: "task_pattern", Title: "Write-time supersession hard-deletes the replaced memory on save",
		What: "a corrected lesson should not linger beside the old one", Learned: "carry supersedes:<id> on save and hard-delete the named row inside the insert transaction, so the corpus forgets the replaced memory with no dangling link", Tags: "supersession save hard-delete"},
	{TaskType: "droids-mem", Kind: "task_pattern", Title: "Make an MCP server self-invoking via the initialize instructions field",
		What: "agents would not call the memory tool without a user prompt", Learned: "put usage guidance in the MCP initialize instructions field so the host model calls the tool proactively; best-effort everywhere, hard guarantees only where the host has hooks", Tags: "mcp instructions self-invoke cross-host"},
	{TaskType: "droids-mem", Kind: "task_pattern", Title: "Build a Go code graph with CHA, not RTA, so library repos still index",
		What: "an RTA call graph indexed empty for a library with no main", Learned: "use callgraph CHA, which needs no main-function roots and over-approximates dispatch, so blast-radius never misses a real caller even in a library package", Tags: "code-graph cha rta callgraph"},
}

// benchQuery is one benchmark query and the title it must retrieve.
type benchQuery struct {
	query, expect, taskType, qtype string
}

// benchQueries: query wording is independent of the target memory. qtype groups
// the report.
var benchQueries = []benchQuery{
	// git
	{"wrong branch base causes conflicts when popping a stash", "Stash pop conflicts mean a wrong branch base, not a content merge", "git", "reorder"},
	{"conflicts after popping the stash because the base was old", "Stash pop conflicts mean a wrong branch base, not a content merge", "git", "reword"},
	{"restoring shelved edits broke since the branch started too far back", "Stash pop conflicts mean a wrong branch base, not a content merge", "git", "synonym_hard"},
	{"get back commits a teammate overwrote by force pushing", "Recover a force-pushed branch with the reflog", "git", "reword"},
	{"undo a clobbered branch using the ref history log", "Recover a force-pushed branch with the reflog", "git", "synonym_hard"},

	// concurrency
	{"goroutine leak channel no receiver", "Goroutine leak from sending on a channel with no receiver", "concurrency", "keyword"},
	{"background workers pile up when nothing consumes what they send", "Goroutine leak from sending on a channel with no receiver", "concurrency", "synonym_hard"},
	{"shared map data race fixed with an RWMutex", "Data race on a shared map fixed with an RWMutex", "concurrency", "reorder"},
	{"concurrent writes to the same dictionary corrupted it", "Data race on a shared map fixed with an RWMutex", "concurrency", "synonym_hard"},
	{"threads froze grabbing two locks in opposite sequence", "Deadlock from acquiring two mutexes in different orders", "concurrency", "synonym_hard"},

	// runtime-panics
	{"unchecked map lookup causes a nil pointer dereference", "Nil pointer dereference on an unchecked map lookup", "runtime-panics", "reorder"},
	{"program crashed on a null reference from a missing key", "Nil pointer dereference on an unchecked map lookup", "runtime-panics", "synonym_hard"},
	{"a typed nil in an interface compares as non-nil", "Nil interface is not nil: the typed-nil comparison trap", "runtime-panics", "reword"},
	{"iterating one index past the end of a slice", "Slice bounds out of range from an off-by-one loop", "runtime-panics", "reword"},

	// http-clients
	{"retry 429 backoff jitter", "Back off and retry on HTTP 429 with jitter", "http-clients", "keyword"},
	{"slow down and try again when the server says too many requests", "Back off and retry on HTTP 429 with jitter", "http-clients", "synonym_hard"},
	{"the http client blocks forever with no timeout set", "HTTP client hangs forever without a request timeout", "http-clients", "reword"},
	{"requests never return because we never set a deadline", "HTTP client hangs forever without a request timeout", "http-clients", "synonym_hard"},

	// performance
	{"N+1 query batched with an IN clause", "N+1 queries fixed by batching with an IN clause", "performance", "reorder"},
	{"one round trip per row was slow so we grouped them into a single fetch", "N+1 queries fixed by batching with an IN clause", "performance", "synonym_hard"},
	{"the database reads every row because the column is not indexed", "Missing index turns a lookup into a full table scan", "performance", "reword"},
	{"allocating inside a tight loop thrashed the collector", "Reuse a buffer to kill allocation in a hot loop", "performance", "reword"},

	// ci
	{"CI red but green locally from a base-merge semantic conflict", "Local-green but CI-red: the base merge introduces a semantic conflict", "ci", "reorder"},
	{"passes on my machine yet the pipeline fails after integrating the target branch", "Local-green but CI-red: the base merge introduces a semantic conflict", "ci", "synonym_hard"},
	{"a test that passes or fails depending on map iteration order", "Flaky test from relying on map iteration order", "ci", "reword"},

	// droids-mem
	{"use a porter stemmer to fix paraphrase misses instead of vectors", "Close the paraphrase retrieval gap with a porter stemmer, no embeddings", "droids-mem", "reword"},
	{"stemming closes paraphrased retrieval without embeddings", "Close the paraphrase retrieval gap with a porter stemmer, no embeddings", "droids-mem", "morphological"},
	{"on save hard-delete the replaced memory via supersession", "Write-time supersession hard-deletes the replaced memory on save", "droids-mem", "reorder"},
	{"when writing a new note wipe the old one it takes the place of", "Write-time supersession hard-deletes the replaced memory on save", "droids-mem", "synonym_hard"},
	{"self-invoking MCP server via the initialize instructions field", "Make an MCP server self-invoking via the initialize instructions field", "droids-mem", "reorder"},
	{"get the tool to call itself automatically on any assistant host", "Make an MCP server self-invoking via the initialize instructions field", "droids-mem", "synonym_hard"},
	{"Go code graph with CHA not RTA", "Build a Go code graph with CHA, not RTA, so library repos still index", "droids-mem", "keyword"},
	{"build a call graph so a library package with no main still gets indexed", "Build a Go code graph with CHA, not RTA, so library repos still index", "droids-mem", "reword"},
}

// benchRules are standing user_rule memories for the load scenario: constraints
// an agent must honour all session (commit conventions, review etiquette, tool
// preferences), worded independently of the topical benchQueries. At most 5 per
// task_type, so mem_context's always tier holds every one.
var benchRules = []store.SaveRequest{
	{TaskType: "git", Kind: "user_rule", Title: "Commit subjects follow Conventional Commits with no AI attribution",
		What: "standing preference from the user", Learned: "write type(scope): summary subjects in the imperative and never append a Co-Authored-By trailer for an assistant"},
	{TaskType: "git", Kind: "user_rule", Title: "Pull request descriptions stay under 200 words",
		What: "standing preference from the user", Learned: "summarize the why in one short paragraph and link the issue; reviewers skip long descriptions"},
	{TaskType: "concurrency", Kind: "user_rule", Title: "Prefer standard library sync primitives over third-party packages",
		What: "standing preference from the user", Learned: "reach for sync and errgroup before adding a dependency for coordination"},
	{TaskType: "concurrency", Kind: "user_rule", Title: "Explain design choices in review comments, not in chat",
		What: "standing preference from the user", Learned: "leave the rationale on the pull request so it survives after the session ends"},
	{TaskType: "runtime-panics", Kind: "user_rule", Title: "Do not use recover to silence failures in tests",
		What: "standing preference from the user", Learned: "a recover that swallows the failure hides the defect; fix the cause and let the test fail loudly"},
	{TaskType: "runtime-panics", Kind: "user_rule", Title: "Reproduce every bug with a failing test before fixing it",
		What: "standing preference from the user", Learned: "write the test first, watch it fail, then change the code until it passes"},
	{TaskType: "http-clients", Kind: "user_rule", Title: "Unit tests never touch the network",
		What: "standing preference from the user", Learned: "stub every upstream with httptest so the suite runs offline and fast"},
	{TaskType: "http-clients", Kind: "user_rule", Title: "Reply to every review thread before asking for re-review",
		What: "standing preference from the user", Learned: "resolve or answer each thread so the reviewer knows what changed and why"},
	{TaskType: "performance", Kind: "user_rule", Title: "Benchmark before and after any optimization",
		What: "standing preference from the user", Learned: "attach go test -bench numbers to the pull request; no unmeasured speedups get merged"},
	{TaskType: "performance", Kind: "user_rule", Title: "Prefer readable code over micro-optimizations",
		What: "standing preference from the user", Learned: "only optimize a path that a profile shows is hot; otherwise keep the obvious version"},
	{TaskType: "ci", Kind: "user_rule", Title: "Lint must be clean before a change is called done",
		What: "standing preference from the user", Learned: "run golangci-lint with a five minute timeout and fix every finding, not just go vet"},
	{TaskType: "ci", Kind: "user_rule", Title: "Never mark a job as allowed to fail",
		What: "standing preference from the user", Learned: "a job whose failure is ignored stops being a signal; fix it or delete it"},
	{TaskType: "droids-mem", Kind: "user_rule", Title: "Keep every change as small as possible",
		What: "standing preference from the user", Learned: "we are a startup: handle only the most important cases and avoid speculative abstraction"},
	{TaskType: "droids-mem", Kind: "user_rule", Title: "Use rg, fd and bat instead of grep, find and cat",
		What: "standing preference from the user", Learned: "shell commands use the modern tools; install them with brew if they are missing"},
}

// loadDistractorsPerTaskType pads every task_type past mem_context's browse
// window, which holds 10 error_resolution + 10 task_pattern rows
// (browseErrorLimit + browseTaskLimit in context.go). The clean corpus has 3-4
// rows per task_type, so there the bundle is the whole project and cannot miss.
// 25 alternating distractors give each task_type 13 error_resolution and 12
// task_pattern rows — more than either per-kind cap — so the bundle has to
// choose, and a target can be left out.
const loadDistractorsPerTaskType = 25

// loadDistractors returns loadDistractorsPerTaskType memories per task_type,
// built from that task_type's own title/what vocabulary so they are realistic
// same-project neighbours. Word choice is deterministic index arithmetic (no
// math/rand), so the report is reproducible.
func loadDistractors() []store.SaveRequest {
	var order []string
	vocab := map[string][]string{}
	for _, m := range benchCorpus {
		if _, ok := vocab[m.TaskType]; !ok {
			order = append(order, m.TaskType)
		}
		for _, w := range strings.Fields(strings.ToLower(m.Title + " " + m.What)) {
			if w = strings.Trim(w, ",.:;()'\""); len(w) > 3 {
				vocab[m.TaskType] = append(vocab[m.TaskType], w)
			}
		}
	}

	var out []store.SaveRequest
	for _, tt := range order {
		words := vocab[tt]
		for i := range loadDistractorsPerTaskType {
			// murmur3 fmix32 of (i, off, j): spreads picks like a seeded RNG
			// would, so distractors stay below the near-duplicate threshold, but
			// is fixed.
			pick := func(n, off int) string {
				out := make([]string, n)
				for j := range out {
					h := uint32(i<<12 | off<<6 | j)
					h ^= h >> 16
					h *= 0x85ebca6b
					h ^= h >> 13
					h *= 0xc2b2ae35
					h ^= h >> 16
					out[j] = words[h%uint32(len(words))]
				}
				return strings.Join(out, " ")
			}
			kind := "error_resolution"
			if i%2 == 1 {
				kind = "task_pattern"
			}
			out = append(out, store.SaveRequest{
				TaskType: tt, Kind: kind,
				Title:   fmt.Sprintf("%s note %d: %s", tt, i, pick(3, 0)),
				What:    pick(6, 11),
				Learned: pick(8, 23),
			})
		}
	}
	return out
}

// loadResult is the load scenario's outcome. Rates are over all benchQueries.
type loadResult struct {
	distractors, rules, total int
	scoped5, scoped20         float64 // mem_search scoped to the query's task_type
	ctxQuery, ctxNoQuery      float64 // mem_context browse tier
	ruleSearch, ruleContext   float64 // a user_rule reached the agent
}

// runLoadScenario seeds benchCorpus + distractors + rules into a fresh store and
// measures the session-start call shapes against the same queries.
func runLoadScenario(t *testing.T, fixtures []store.Fixture) loadResult {
	t.Helper()
	ctx := context.Background()
	s := newTestStore(t)
	distractors := loadDistractors()
	seed := slices.Concat(benchCorpus, distractors, benchRules)
	for _, m := range seed {
		resp, err := s.Save(ctx, m)
		if err != nil {
			t.Fatalf("seed load scenario: %v", err)
		}
		// A deduped distractor silently shrinks the load; every row must land.
		if resp.Status != "saved" {
			t.Fatalf("seed %q not inserted: status=%s reason=%s matched=%q", m.Title, resp.Status, resp.Reason, resp.MatchedTitle)
		}
	}

	rep, err := s.Eval(ctx, fixtures)
	if err != nil {
		t.Fatalf("Eval (load): %v", err)
	}
	if rep.Unresolved != 0 || rep.Ambiguous != 0 {
		t.Fatalf("load fixtures out of sync: %d unresolved, %d ambiguous", rep.Unresolved, rep.Ambiguous)
	}

	var hit5, hit20, ruleSearch, ruleContext int
	for _, q := range benchQueries {
		// 20 is mem_search's max page: the same slot budget as the 10+10 browse tier.
		resp, err := s.Search(ctx, store.SearchRequest{Query: q.query, TaskType: q.taskType, Limit: 20})
		if err != nil {
			t.Fatalf("scoped search: %v", err)
		}
		ruleSeen := false
		for i, r := range resp.Results {
			if r.Title == q.expect {
				hit20++
				if i < 5 {
					hit5++
				}
			}
			if i < 5 && r.Kind == "user_rule" {
				ruleSeen = true
			}
		}
		if ruleSeen {
			ruleSearch++
		}

		bundle, err := s.Context(ctx, store.ContextRequest{TaskType: q.taskType, Query: q.query, Mode: store.ModeOrient})
		if err != nil {
			t.Fatalf("context: %v", err)
		}
		if len(bundle.UserRules) > 0 {
			ruleContext++
		}
	}

	n := float64(len(benchQueries))
	return loadResult{
		distractors: loadDistractorsPerTaskType,
		rules:       len(benchRules),
		total:       len(seed),
		scoped5:     float64(hit5) / n,
		scoped20:    float64(hit20) / n,
		ctxQuery:    rep.MemContext.BrowseHitRate,
		ctxNoQuery:  rep.MemContext.BrowseHitRateNoQuery,
		ruleSearch:  float64(ruleSearch) / n,
		ruleContext: float64(ruleContext) / n,
	}
}

func TestRecallBenchmark(t *testing.T) {
	s := newTestStore(t)
	for _, m := range benchCorpus {
		if _, err := s.Save(context.Background(), m); err != nil {
			t.Fatalf("seed benchmark corpus: %v", err)
		}
	}

	fixtures := make([]store.Fixture, len(benchQueries))
	for i, q := range benchQueries {
		fixtures[i] = store.Fixture{Query: q.query, ExpectTitle: q.expect, TaskType: q.taskType, Type: q.qtype}
	}

	rep, err := s.Eval(context.Background(), fixtures)
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}

	// A benchmark query that doesn't resolve is an authoring bug in this file,
	// not a retrieval result — fail loudly so the corpus and queries stay in sync.
	if rep.Unresolved != 0 || rep.Ambiguous != 0 {
		t.Fatalf("benchmark fixtures out of sync with corpus: %d unresolved, %d ambiguous (check exact titles)", rep.Unresolved, rep.Ambiguous)
	}

	load := runLoadScenario(t, fixtures)

	md := renderBenchmark(rep, len(benchCorpus)) + renderLoad(load, len(benchQueries))
	t.Log("\n" + md)

	path := filepath.Join("..", "..", "eval", "RESULTS.md")
	if os.Getenv("EVAL_WRITE_REPORT") != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("mkdir eval: %v", err)
		}
		if err := os.WriteFile(path, []byte(md), 0o600); err != nil {
			t.Fatalf("write RESULTS.md: %v", err)
		}
		t.Logf("wrote %s", path)
	} else {
		// RESULTS.md is a committed artifact, so it silently rots: a change to
		// ranking, the corpus, or the report shape leaves stale numbers in the
		// repo that nobody notices until someone reads them as current. The
		// render is deterministic on a fixed corpus, so treat the committed file
		// as a golden — drift fails here, in the same run that caused it.
		committed, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read RESULTS.md (regenerate with EVAL_WRITE_REPORT=1): %v", err)
		}
		if string(committed) != md {
			t.Errorf("eval/RESULTS.md is stale — regenerate and commit it:\n"+
				"\tEVAL_WRITE_REPORT=1 go test ./internal/store -run TestRecallBenchmark -count=1\n\n"+
				"current run produced:\n%s", md)
		}
	}

	// Regression floors — guard the marketed claim without asserting perfection.
	// These only catch a real collapse of the paraphrase capability.
	assertFloor(t, "mem_search recall@5 overall", rep.MemSearch.RecallAt5, 0.95)
	assertFloor(t, "mem_search MRR overall", rep.MemSearch.MRR, 0.85)
	if tm := rep.ByType["synonym_hard"]; tm != nil {
		// Weakest class by construction (zero surface overlap with the target).
		// Floored only high enough to lock in the column-weighted BM25 gain.
		assertFloor(t, "synonym_hard recall@5", tm.RecallAt5, 0.90)
	}
	if tm := rep.ByType["reorder"]; tm != nil {
		assertFloor(t, "reorder recall@5", tm.RecallAt5, 0.95)
	}
	if tm := rep.ByType["morphological"]; tm != nil {
		assertFloor(t, "morphological recall@5", tm.RecallAt5, 0.95)
	}

	// Load-scenario floors. No floor on load.ctxNoQuery: it documents a known
	// collapse (the no-query bundle ranks by recency), not a capability to guard.
	assertFloor(t, "load: mem_context with query", load.ctxQuery, 0.95)
	assertFloor(t, "load: scoped mem_search recall@20", load.scoped20, 0.95)
	assertFloor(t, "load: rule recall via mem_context", load.ruleContext, 1.0)
}

func assertFloor(t *testing.T, name string, got, floor float64) {
	t.Helper()
	if got < floor {
		t.Errorf("REGRESSION: %s = %.2f, below floor %.2f", name, got, floor)
	}
}

// renderBenchmark formats the report as markdown.
//
// Data plus definitions, never claims. 4ffe33d stripped this report back to
// bare tables to get rid of editorializing ("the honest limit…", "proof we are
// not cherry-picking") — that judgement stands and is not reversed here. But it
// also removed the metric and column definitions, which left a reader who does
// not already know the harness unable to say what `overlap 0.17` or an MRR of
// 0.90 means. Definitions are documentation, not narrative: they describe how a
// number was computed without asserting anything about whether it is good.
func renderBenchmark(rep *store.EvalReport, corpusSize int) string {
	order := []struct{ key, label string }{
		{"keyword", "keyword"},
		{"morphological", "morphological"},
		{"reorder", "word-order"},
		{"reword", "reword"},
		{"synonym_hard", "synonym (zero overlap)"},
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Retrieval benchmark\n\n")
	fmt.Fprintf(&b, "Measures whether droids-mem returns the *right* memory for a query "+
		"phrased in words the author never wrote. Retrieval is lexical (SQLite FTS5 + "+
		"porter stemming, no embeddings), so paraphrase is the hard case.\n\n")
	fmt.Fprintf(&b, "Generated from `internal/store/recall_benchmark_test.go`. Regenerate with:\n\n")
	fmt.Fprintf(&b, "```\nEVAL_WRITE_REPORT=1 go test ./internal/store -run TestRecallBenchmark -count=1\n```\n\n")

	fmt.Fprintf(&b, "## Setup\n\n")
	fmt.Fprintf(&b, "**Corpus** — %d memories in 7 topic clusters. Memories inside a cluster share "+
		"vocabulary, so a query has to beat its near-neighbours, not just unrelated rows.\n\n", corpusSize)
	fmt.Fprintf(&b, "**Queries** — %d, each hand-authored to name exactly one correct memory and "+
		"worded independently of it.\n\n", rep.TotalPairs)

	fmt.Fprintf(&b, "**Query classes** — how a query is related to its target:\n\n")
	fmt.Fprintf(&b, "| class | relation to the target's wording |\n|---|---|\n")
	fmt.Fprintf(&b, "| keyword | shares distinctive terms |\n")
	fmt.Fprintf(&b, "| morphological | same terms, different inflection (exercises porter stemming) |\n")
	fmt.Fprintf(&b, "| word-order | same terms, reordered |\n")
	fmt.Fprintf(&b, "| reword | partly reworded; some terms survive |\n")
	fmt.Fprintf(&b, "| synonym (zero overlap) | shares no terms at all — only meaning |\n\n")

	fmt.Fprintf(&b, "**Metrics**:\n\n")
	fmt.Fprintf(&b, "| metric | meaning |\n|---|---|\n")
	fmt.Fprintf(&b, "| recall@1 | target came back first, ahead of every distractor |\n")
	fmt.Fprintf(&b, "| recall@5 | target came back within the top 5 — `mem_search`'s default page |\n")
	fmt.Fprintf(&b, "| MRR | mean reciprocal rank: 1.00 = always first, 0.50 = typically second |\n\n")

	fmt.Fprintf(&b, "## mem_search\n\n")
	fmt.Fprintf(&b, "| query class | n | recall@1 | recall@5 | MRR |\n")
	fmt.Fprintf(&b, "|---|---|---|---|---|\n")
	for _, o := range order {
		tm := rep.ByType[o.key]
		if tm == nil || tm.N == 0 {
			continue
		}
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s |\n",
			o.label, tm.N, pct(tm.RecallAt1), pct(tm.RecallAt5), dec(tm.MRR))
	}
	fmt.Fprintf(&b, "| overall | %d | %s | %s | %s |\n\n",
		rep.MemSearch.Scored, pct(rep.MemSearch.RecallAt1), pct(rep.MemSearch.RecallAt5), dec(rep.MemSearch.MRR))

	fmt.Fprintf(&b, "## mem_context browse tier\n\n")
	fmt.Fprintf(&b, "`mem_context` returns a bundle, not a ranked list. `browse_hit_rate` is the "+
		"share of eligible pairs whose target appears anywhere in the bundle's browse tier.\n\n")
	fmt.Fprintf(&b, "A pair is **eligible** when its target is structurally reachable there: a "+
		"browse-tier kind (`error_resolution` or `task_pattern`) carrying a `task_type`. "+
		"Pairs that can never appear are excluded rather than counted as failures.\n\n")
	fmt.Fprintf(&b, "| call shape | hit rate | eligible pairs |\n|---|---|---|\n")
	fmt.Fprintf(&b, "| with query | %s | %d |\n", pct(rep.MemContext.BrowseHitRate), rep.MemContext.EligiblePairs)
	fmt.Fprintf(&b, "| no query (session-start default) | %s | %d |\n\n",
		pct(rep.MemContext.BrowseHitRateNoQuery), rep.MemContext.EligiblePairs)

	fmt.Fprintf(&b, "## Misses — target outside `mem_search`'s top 5\n\n")
	misses := searchMisses(rep)
	if len(misses) == 0 {
		fmt.Fprintf(&b, "None.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "- **rank** — position the target was returned at; `—` means it was not returned at all.\n")
	fmt.Fprintf(&b, "- **overlap** — share of the query's words (>2 chars) that literally appear in the "+
		"target's title, what, or learned text. 0.00 means the query and the memory have no words "+
		"in common, so only meaning connects them.\n\n")
	fmt.Fprintf(&b, "| rank | overlap | class | query | target |\n")
	fmt.Fprintf(&b, "|---|---|---|---|---|\n")
	// Label the class the same way the results table does; the raw fixture keys
	// (synonym_hard, reorder) are internal names and mean nothing to a reader.
	label := map[string]string{}
	for _, o := range order {
		label[o.key] = o.label
	}
	for _, m := range misses {
		class := label[m.Type]
		if class == "" {
			class = m.Type
		}
		fmt.Fprintf(&b, "| %s | %.2f | %s | %s | %s |\n",
			rankStr(m.SearchRank), m.QueryTargetOverlap, class, m.Query, m.ExpectTitle)
	}
	return b.String()
}

// renderLoad formats the load scenario as a markdown section appended after the
// clean-corpus report.
func renderLoad(l loadResult, queries int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n## Session-start recall under load\n\n")
	fmt.Fprintf(&b, "The clean corpus holds 3-4 memories per `task_type`, while `mem_context`'s browse "+
		"tier holds up to 10 `error_resolution` + 10 `task_pattern` rows. There the bundle is the "+
		"whole project, so the browse-tier rates above cannot fall below 100%%.\n\n")
	fmt.Fprintf(&b, "This scenario adds %d distractors per `task_type`, built from that task_type's own "+
		"vocabulary and alternating `error_resolution` / `task_pattern` so both per-kind caps "+
		"overflow, plus %d standing `user_rule` memories (commit conventions, review etiquette, tool "+
		"preferences) worded independently of the queries. %d memories in total; the same %d queries.\n\n",
		l.distractors, l.rules, l.total, queries)

	fmt.Fprintf(&b, "**Topical recall** — share of queries whose target is returned:\n\n")
	fmt.Fprintf(&b, "| call shape | slots | hit rate |\n|---|---|---|\n")
	fmt.Fprintf(&b, "| `mem_search` scoped to the query's task_type | 5 | %s |\n", pct(l.scoped5))
	fmt.Fprintf(&b, "| `mem_search` scoped to the query's task_type | 20 | %s |\n", pct(l.scoped20))
	fmt.Fprintf(&b, "| `mem_context` browse tier, with query | 20 | %s |\n", pct(l.ctxQuery))
	fmt.Fprintf(&b, "| `mem_context` browse tier, no query | 20 | %s |\n\n", pct(l.ctxNoQuery))
	fmt.Fprintf(&b, "Without a query the browse tier falls back to recency: the newest rows fill both "+
		"per-kind caps and older targets are evicted. With a query the same 20 slots are ranked by "+
		"BM25 within the task_type.\n\n")

	fmt.Fprintf(&b, "**Rule recall** — share of queries for which at least one `user_rule` of the "+
		"query's task_type reaches the agent:\n\n")
	fmt.Fprintf(&b, "| where | rate |\n|---|---|\n")
	fmt.Fprintf(&b, "| `mem_search` scoped, top 5 | %s |\n", pct(l.ruleSearch))
	fmt.Fprintf(&b, "| `mem_context` always tier | %s |\n\n", pct(l.ruleContext))
	fmt.Fprintf(&b, "The always tier returns the task_type's newest 5 rules regardless of the query. "+
		"Search returns a rule only when the query happens to share its terms, so rules reach the "+
		"agent through `mem_context`'s always tier.\n")
	return b.String()
}

// searchMisses returns resolved pairs whose target was not in mem_search's top 5,
// worst rank first (0 = absent from the result set, treated as worst).
func searchMisses(rep *store.EvalReport) []store.PairResult {
	var out []store.PairResult
	for _, p := range rep.Pairs {
		if p.Resolution == "ok" && !p.SearchHitAt5 {
			out = append(out, p)
		}
	}
	rankKey := func(r int) int {
		if r == 0 {
			return 1 << 30
		}
		return r
	}
	sort.Slice(out, func(i, j int) bool { return rankKey(out[i].SearchRank) > rankKey(out[j].SearchRank) })
	return out
}

func pct(f float64) string { return fmt.Sprintf("%.0f%%", f*100) }
func dec(f float64) string { return fmt.Sprintf("%.2f", f) }
func rankStr(r int) string {
	if r == 0 {
		return "—"
	}
	return fmt.Sprintf("%d", r)
}
