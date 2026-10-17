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
| synonym (zero overlap) | 12 | 58% | 92% | 0.70 |
| overall | 33 | 85% | 97% | 0.89 |

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

## Session-start recall under load

The clean corpus holds 3-4 memories per `task_type`, while `mem_context`'s browse tier holds up to 10 `error_resolution` + 10 `task_pattern` rows. There the bundle is the whole project, so the browse-tier rates above cannot fall below 100%.

This scenario adds 25 distractors per `task_type`, built from that task_type's own vocabulary and alternating `error_resolution` / `task_pattern` so both per-kind caps overflow, plus 14 standing `user_rule` memories (commit conventions, review etiquette, tool preferences) worded independently of the queries. 213 memories in total; the same 33 queries.

**Topical recall** — share of queries whose target is returned:

| call shape | slots | hit rate |
|---|---|---|
| `mem_search` scoped to the query's task_type | 5 | 97% |
| `mem_search` scoped to the query's task_type | 20 | 100% |
| `mem_context` browse tier, with query | 20 | 100% |
| `mem_context` browse tier, no query | 20 | 0% |

Without a query the browse tier falls back to recency: the newest rows fill both per-kind caps and older targets are evicted. With a query the same 20 slots are ranked by BM25 within the task_type.

**Rule recall** — share of queries for which at least one `user_rule` of the query's task_type reaches the agent:

| where | rate |
|---|---|
| `mem_search` scoped, top 5 | 18% |
| `mem_context` always tier | 100% |

The always tier returns the task_type's newest 5 rules regardless of the query. Search returns a rule only when the query happens to share its terms, so rules reach the agent through `mem_context`'s always tier.

**Session-start protocol: before vs after** — the calls an agent makes at task start when following each version of the server instructions, on the same store and queries. Topical: the target came back from any call. Rule: a `user_rule` of the query's task_type did. Bytes: JSON size of the store responses per session start (compact search rows, context bundle), excluding the MCP envelope.

| protocol | calls | topical | rule | mean bytes |
|---|---|---|---|---|
| old, core loop only: `mem_search` unscoped, default limit | 1 | 91% | 6% | 1458 |
| old, full text: + `mem_context` without query | 2 | 91% | 100% | 7171 |
| new: `mem_context` with query | 1 | 100% | 100% | 5500 |

An agent that reads only the old core loop gets the cheapest response but almost never sees a standing rule, and an unscoped top 5 lets other projects' memories crowd out the target. The old full text recovers the rules only with a second call whose recency-ranked bundle adds no topical hits. One `mem_context` call with a query returns both the target and the rules, in fewer bytes than the old two-call protocol.
