# Retrieval benchmark

Corpus: 24 memories, 7 clusters. Queries: 33.

## mem_search

| query class | n | recall@1 | recall@5 | MRR |
|---|---|---|---|---|
| keyword | 3 | 100% | 100% | 1.00 |
| morphological | 1 | 100% | 100% | 1.00 |
| word-order | 7 | 100% | 100% | 1.00 |
| reword | 10 | 100% | 100% | 1.00 |
| synonym (zero overlap) | 12 | 58% | 92% | 0.70 |
| overall | 33 | 85% | 97% | 0.89 |

## mem_context browse tier

browse_hit_rate: 100% (33 eligible pairs)
browse_hit_rate (no query): 100% (33 eligible pairs)

## misses (rank > 5)

| rank | overlap | class | query | target |
|---|---|---|---|---|
| 8 | 0.17 | synonym_hard | slow down and try again when the server says too many requests | Back off and retry on HTTP 429 with jitter |
