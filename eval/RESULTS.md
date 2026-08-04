# Retrieval benchmark

Measures whether droids-mem returns the *right* memory for a query phrased in words the author never wrote. Retrieval is lexical (SQLite FTS5 + porter stemming, no embeddings), so paraphrase is the hard case.

Generated from `internal/store/recall_benchmark_test.go`. Regenerate with:

```
EVAL_WRITE_REPORT=1 go test ./internal/store -run TestRecallBenchmark -count=1
```

## Setup

**Corpus** — 24 memories in 7 topic clusters. Memories inside a cluster share vocabulary, so a query has to beat its near-neighbours, not just unrelated rows.

**Queries** — 33, each hand-authored to name exactly one correct memory and worded independently of it.

**Query classes** — how a query is related to its target:

| class | relation to the target's wording |
|---|---|
| keyword | shares distinctive terms |
| morphological | same terms, different inflection (exercises porter stemming) |
| word-order | same terms, reordered |
| reword | partly reworded; some terms survive |
| synonym (zero overlap) | shares no terms at all — only meaning |

**Metrics**:

| metric | meaning |
|---|---|
| recall@1 | target came back first, ahead of every distractor |
| recall@5 | target came back within the top 5 — `mem_search`'s default page |
| MRR | mean reciprocal rank: 1.00 = always first, 0.50 = typically second |

## mem_search

| query class | n | recall@1 | recall@5 | MRR |
|---|---|---|---|---|
| keyword | 3 | 100% | 100% | 1.00 |
| morphological | 1 | 100% | 100% | 1.00 |
| word-order | 7 | 100% | 100% | 1.00 |
| reword | 10 | 100% | 100% | 1.00 |
| synonym (zero overlap) | 12 | 67% | 75% | 0.73 |
| overall | 33 | 88% | 91% | 0.90 |

## mem_context browse tier

`mem_context` returns a bundle, not a ranked list. `browse_hit_rate` is the share of eligible pairs whose target appears anywhere in the bundle's browse tier.

A pair is **eligible** when its target is structurally reachable there: a browse-tier kind (`error_resolution` or `task_pattern`) carrying a `task_type`. Pairs that can never appear are excluded rather than counted as failures.

| call shape | hit rate | eligible pairs |
|---|---|---|
| with query | 100% | 33 |
| no query (session-start default) | 100% | 33 |

## Misses — target outside `mem_search`'s top 5

- **rank** — position the target was returned at; `—` means it was not returned at all.
- **overlap** — share of the query's words (>2 chars) that literally appear in the target's title, what, or learned text. 0.00 means the query and the memory have no words in common, so only meaning connects them.

| rank | overlap | class | query | target |
|---|---|---|---|---|
| 8 | 0.17 | synonym (zero overlap) | slow down and try again when the server says too many requests | Back off and retry on HTTP 429 with jitter |
| 7 | 0.18 | synonym (zero overlap) | restoring shelved edits broke since the branch started too far back | Stash pop conflicts mean a wrong branch base, not a content merge |
| 6 | 0.25 | synonym (zero overlap) | undo a clobbered branch using the ref history log | Recover a force-pushed branch with the reflog |
