# LEARNINGS.codex.md

## 2026-07-04T11:40:09+08:00 - Module 3 Agent API Integration

Perceive: the Agent Orchestrator core was implemented and tested, but it was still only an internal library. The platform needed a usable boundary where clients can create, inspect, resume, and approve Agent runs.

Reason: a production Agent system should keep the state-machine core independent from HTTP and JWT concerns. The first business tool should reuse an existing trusted capability, so the safest integration path is a read-only `rag_query` tool backed by the existing Query Service before introducing an LLM planner.

Act: added `query.Service.Ask`, propagated authenticated user/scopes, added `internal/agentapi`, registered the first RAG tool, added deterministic one-step planning, mounted `/v1/agent/runs`, added Agent config, and normalized Agent API metrics.

Refine: added tests for create-and-execute, tenant-scoped lookup, pending approval resume, and Agent config defaults/overrides. Remaining work is the real LLM planner, external approval queue/audit, additional tools, and distributed lock manager.

## 2026-06-28T16:51:16+08:00 - Enterprise Rerank Policy

Perceive: enabling reranker globally improved the architecture but regressed deterministic exact-token evaluation cases because Cross-Encoder scoring overrode strong keyword and identifier matches.

Reason: enterprise RAG systems usually treat reranking as a routed second-stage ranker, not as a universal replacement for exact retrieval. Exact identifiers, order numbers, trace IDs, and error codes should keep lexical or fused ranking unless an experiment explicitly asks otherwise.

Act: added `RETRIEVAL_RERANK_POLICY=auto|always`, routed only semantic and hybrid queries through reranker in `auto`, kept fused ranking for exact intent, and documented the operational behavior.

Refine: unit tests cover the policy branches and config validation; full verification remains the Go test suite plus Docker Compose config and deterministic eval rerun.

## 2026-06-28T18:20:43+08:00 - Rerank Policy Benefit/Risk Eval

Perceive: the previous golden eval proved that `auto` avoids exact-token regression, but it did not prove that semantic queries benefit from reranking because the golden set is dominated by anchor-token retrieval cases.

Reason: a useful enterprise rerank gate needs both sides of evidence: semantic or fuzzy queries can improve when a cross-encoder sees richer query-document relevance, while exact identifiers should keep lexical/vector fusion when that signal is stronger.

Act: added deterministic retrieval-engine tests that compare rerank-off, `auto`, and `always` behavior with fake retrievers and a scoring reranker. The semantic case proves rerank promotion; the exact case proves `auto` skips a reranker that would otherwise regress the top result.

Refine: documented the focused rerank policy eval command in `docs/evals/README.md`; the broader golden eval still remains the regression gate for full-stack behavior.

## 2026-06-29T18:15:11+08:00 - Candidate-Aware Exact Evidence Protection

Perceive: query-only keyword and regex routing cannot enumerate every enterprise identifier shape, so relying only on `RouteQuery` leaves gaps for domain-specific IDs.

Reason: exact-match protection should use both query intent and retrieval evidence. If a strong token from the user query is present in retrieved `doc_id`, `chunk_id`, or content, that candidate carries deterministic evidence that a cross-encoder should not casually override.

Act: added broad exact-token extraction for emails, UUIDs, phone-like numbers, structured IDs, and mixed alphanumeric tokens with separators; added candidate evidence checks with separator normalization; `auto` now skips reranking for semantic/hybrid routes when exact candidate evidence exists.

Refine: added tests proving an unrouted exact token query remains semantic under old route rules but is still protected because the fused candidates contain the exact token.

## 2026-06-29T19:20:00+08:00 - Full Enterprise Rerank Guardrail

Perceive: the candidate-aware policy still treated exact evidence as a rerank skip, and it did not carry business schema fields through ingestion, storage, retrieval, and evaluation.

Reason: enterprise RAG needs layered protection rather than a larger keyword list. Rules catch obvious intent, schema fields carry business identifiers, retrieval evidence proves candidate-level exact matches, and protective rerank prevents a cross-encoder from demoting deterministic evidence.

Act: added metadata propagation from upload to parser chunks, Qdrant, Elasticsearch, and candidates; added `RETRIEVAL_EXACT_SCHEMA_FIELDS`; added ES metadata exact-term recall; changed `auto` to rerank semantic/hybrid exact-evidence cases with exact-match pinning; added privacy-safe rerank decision logs.

Refine: added unit tests for schema metadata evidence, Qdrant/ES metadata extraction, protective pinning, and engine behavior; extended the golden eval set with a metadata-only exact retrieval case.

## 2026-06-29T19:53:41+08:00 - No-Reranker Exact Candidate Pinning

Perceive: CI defaults do not enable the optional reranker service, so the metadata-only golden eval case could recall the exact candidate but still return a higher fused distractor.

Reason: exact-evidence protection is an enterprise retrieval invariant, not a side effect of calling a reranker. Business identifiers in configured schema metadata must be protected in both reranker-enabled and reranker-disabled deployments.

Act: applied the existing exact-evidence planner when the reranker is unconfigured and pinned exact candidates directly over fused ranking; added an engine regression test for schema metadata evidence with the reranker disabled.

Refine: verified targeted retrieval tests, full Go tests, and the deterministic eval with `RETRIEVAL_ENABLE_RERANK=false`, all passing with the metadata-only case included.

## 2026-06-29T21:01:13+08:00 - Module 2 Lightweight Load Test

Perceive: module 2 was functionally complete, but it needed lightweight latency and throughput evidence across cache miss, reranker enabled, cache hit, and schema exact paths.

Reason: benchmark runs are useful not only for numbers but also for finding policy gaps under realistic candidate competition. The schema exact load test showed that an exact route could detect exact evidence in a non-top candidate while still leaving a distractor first because exact routes skipped reranker without applying pinning.

Act: extended the load-test script with scenario labels, noise documents, metadata upload, top-hit rate, and throughput reporting; fixed exact-route candidate pinning by applying `protectExactMatches` whenever the auto policy marks exact evidence protection, even if `ShouldRerank=false`.

Refine: reran module-2 load scenarios and Go verification. Final lightweight results: cache-miss rerank-off p95 49.67 ms, cache-miss rerank-on p95 321.26 ms, cache-hit p95 15.83 ms, schema exact p95 48.79 ms, all with 100% success and 100% top-hit rate.
