# LEARNINGS.codex.md

## 2026-06-28T16:51:16+08:00 - Enterprise Rerank Policy

Perceive: enabling reranker globally improved the architecture but regressed deterministic exact-token evaluation cases because Cross-Encoder scoring overrode strong keyword and identifier matches.

Reason: enterprise RAG systems usually treat reranking as a routed second-stage ranker, not as a universal replacement for exact retrieval. Exact identifiers, order numbers, trace IDs, and error codes should keep lexical or fused ranking unless an experiment explicitly asks otherwise.

Act: added `RETRIEVAL_RERANK_POLICY=auto|always`, routed only semantic and hybrid queries through reranker in `auto`, kept fused ranking for exact intent, and documented the operational behavior.

Refine: unit tests cover the policy branches and config validation; full verification remains the Go test suite plus Docker Compose config and deterministic eval rerun.
