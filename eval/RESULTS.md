# droids-mem retrieval benchmark

Corpus: **24 memories** in 7 distractor clusters. Queries: **33**, hand-authored so their wording is independent of the target memory. Each query must retrieve the right memory ahead of its cluster neighbours. Report-only; the harness lives in `internal/store/recall_benchmark_test.go`.

## mem_search — retrieval by paraphrased query

| Query class | n | recall@1 | recall@5 | MRR |
|---|---|---|---|---|
| keyword (some overlap) | 3 | 100% | 100% | 1.00 |
| morphological (porter) | 1 | 100% | 100% | 1.00 |
| word-order | 7 | 100% | 100% | 1.00 |
| partial reword | 10 | 100% | 100% | 1.00 |
| synonym (zero overlap) | 12 | 67% | 75% | 0.73 |
| **overall** | 33 | **88%** | **91%** | **0.90** |

- **recall@1** — right memory returned *first*, ahead of every distractor.
- **recall@5** — right memory in the top 5 (mem_search's default page).
- **MRR** — mean reciprocal rank; 1.0 = always first.

## The honest limit: pure synonyms

`synonym (zero overlap)` queries share **no words at all** with their target — a lexical index (FTS5 + porter, no embeddings by design) can only bridge these by luck. This is the known ceiling. Queries that missed the top 5 (proof we are not cherry-picking):

- rank 9 · overlap 0.17 · "slow down and try again when the server says too many requests" → "Back off and retry on HTTP 429 with jitter"
- rank 8 · overlap 0.25 · "undo a clobbered branch using the ref history log" → "Recover a force-pushed branch with the reflog"
- rank 7 · overlap 0.18 · "restoring shelved edits broke since the branch started too far back" → "Stash pop conflicts mean a wrong branch base, not a content merge"

