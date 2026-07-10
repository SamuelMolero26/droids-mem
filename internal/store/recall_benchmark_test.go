package store_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

	md := renderBenchmark(rep, len(benchCorpus))
	t.Log("\n" + md)

	if os.Getenv("EVAL_WRITE_REPORT") != "" {
		path := filepath.Join("..", "..", "eval", "RESULTS.md")
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("mkdir eval: %v", err)
		}
		if err := os.WriteFile(path, []byte(md), 0o600); err != nil {
			t.Fatalf("write RESULTS.md: %v", err)
		}
		t.Logf("wrote %s", path)
	}

	// Regression floors — guard the marketed claim without asserting perfection.
	// The honest weak spot (synonym_hard) is deliberately NOT floored high; these
	// only catch a real collapse of the paraphrase capability.
	assertFloor(t, "mem_search recall@5 overall", rep.MemSearch.RecallAt5, 0.75)
	assertFloor(t, "mem_search MRR overall", rep.MemSearch.MRR, 0.65)
	if tm := rep.ByType["reorder"]; tm != nil {
		assertFloor(t, "reorder recall@5", tm.RecallAt5, 0.95)
	}
	if tm := rep.ByType["morphological"]; tm != nil {
		assertFloor(t, "morphological recall@5", tm.RecallAt5, 0.95)
	}
}

func assertFloor(t *testing.T, name string, got, floor float64) {
	t.Helper()
	if got < floor {
		t.Errorf("REGRESSION: %s = %.2f, below floor %.2f", name, got, floor)
	}
}

// renderBenchmark formats the report as markdown: data only.
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
	fmt.Fprintf(&b, "Corpus: %d memories, 7 clusters. Queries: %d.\n\n", corpusSize, rep.TotalPairs)

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
	fmt.Fprintf(&b, "browse_hit_rate: %s (%d eligible queries)\n\n", pct(rep.MemContext.BrowseHitRate), rep.MemContext.EligiblePairs)

	fmt.Fprintf(&b, "## misses (rank > 5)\n\n")
	misses := searchMisses(rep)
	if len(misses) == 0 {
		fmt.Fprintf(&b, "none\n")
		return b.String()
	}
	fmt.Fprintf(&b, "| rank | overlap | class | query | target |\n")
	fmt.Fprintf(&b, "|---|---|---|---|---|\n")
	for _, m := range misses {
		fmt.Fprintf(&b, "| %s | %.2f | %s | %s | %s |\n",
			rankStr(m.SearchRank), m.QueryTargetOverlap, m.Type, m.Query, m.ExpectTitle)
	}
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
