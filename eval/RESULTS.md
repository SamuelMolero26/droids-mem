# Retrieval benchmark

Corpus: 24 memories, 7 clusters. Queries: 33.

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

browse_hit_rate: 100% (33 eligible queries)

## misses (rank > 5)

| rank | overlap | class | query | target |
|---|---|---|---|---|
| 9 | 0.17 | synonym_hard | slow down and try again when the server says too many requests | Back off and retry on HTTP 429 with jitter |
| 8 | 0.25 | synonym_hard | undo a clobbered branch using the ref history log | Recover a force-pushed branch with the reflog |
| 7 | 0.18 | synonym_hard | restoring shelved edits broke since the branch started too far back | Stash pop conflicts mean a wrong branch base, not a content merge |
