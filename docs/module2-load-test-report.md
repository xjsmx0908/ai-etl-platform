# Module 2 Lightweight Load Test Report

Timestamp: 2026-06-29T21:01:13+08:00

Scope: lightweight benchmark for the Hybrid Retrieval & Reranking Engine. Each scenario used 5 concurrent clients, 40 `/v1/query` requests, deterministic mock embedding/LLM endpoints, one target document, and three noise documents.

| Scenario | Rerank | Semantic Cache | Query Type | Success | Top Hit | Throughput | p50 | p95 | p99 |
|---|---:|---:|---|---:|---:|---:|---:|---:|---:|
| cache miss baseline | off | off | hybrid semantic | 100% | 100% | 122.72 qps | 44.95 ms | 49.67 ms | 53.12 ms |
| cache miss with reranker | on | off | hybrid semantic | 100% | 100% | 21.95 qps | 204.07 ms | 321.26 ms | 371.35 ms |
| cache hit with reranker enabled | on | on | hybrid semantic | 100% | 100% | 475.17 qps | 9.00 ms | 15.83 ms | 17.99 ms |
| schema exact with reranker enabled | on | off | metadata-only `customer_ref` | 100% | 100% | 112.93 qps | 45.92 ms | 48.79 ms | 51.98 ms |

Observations:

1. Baseline hybrid retrieval stayed below 50 ms p95 at this small concurrency level.
2. CPU Cross-Encoder reranking is the dominant latency cost: p95 increased from 49.67 ms to 321.26 ms.
3. Semantic cache materially reduces latency even when reranker is enabled; query-api logs confirmed `retrieval_cache_hit=true` during the cache-hit run.
4. The schema exact scenario exposed and verified an important guardrail: exact-route candidates with business metadata evidence must be pinned even when auto policy skips reranker.

Action taken during the run:

The first schema exact run found that an `exact_keyword` route could detect exact evidence in a non-top candidate but still leave a noise document first because exact routes skipped reranker without applying pinning. The retrieval policy now returns `exact_keyword_candidate_pinned`, and the engine applies `protectExactMatches` even when `ShouldRerank=false`.

Verification:

- `python3 -m py_compile scripts/load-test.py`
- `go test ./internal/retrieval -run 'TestEngineRerankPolicy|TestPlanRerankPolicy|TestProtectExactMatches|TestExactCandidateEvidence' -v`
- `go test ./...`
- `RETRIEVAL_ENABLE_RERANK=false RETRIEVAL_RERANK_POLICY=auto RERANK_ENDPOINT= python3 scripts/run-evals.py --keep-services --report-dir /tmp/ai-etl-module2-loadtest-eval --max-wait 180`

Deterministic eval result: 47/47 passed, `pass_rate=100.00%`, `answer_pass_rate=100.00%`, `recall@5=100.00%`.
