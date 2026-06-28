# LEARNINGS.codex.md

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
