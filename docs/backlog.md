# Backlog

## 2026-06-28 - Enterprise Rerank Policy

Status: implemented

Goal: make the optional Cross-Encoder reranker behave like an enterprise retrieval stage instead of reranking every query.

Plan:

1. Add `RETRIEVAL_RERANK_POLICY` with `auto` as the default and `always` for offline experiments.
2. Keep exact identifier and keyword queries on fused BM25/vector ranking in `auto` mode.
3. Allow semantic and hybrid queries to call the reranker when the service is enabled and configured.
4. Preserve fallback behavior: if the reranker fails, return fused ranking with a partial error.
5. Cover the policy with unit tests and update operator-facing documentation.

## 2026-06-28 - Rerank Policy Benefit/Risk Eval

Status: implemented

Goal: prove the `auto` policy is not only avoiding regressions, but is routing by the cases where reranking helps or hurts.

Plan:

1. Add a deterministic retrieval-engine test where semantic fused ranking is intentionally worse than reranked order.
2. Add a deterministic exact-query test where fused BM25/vector ranking is better and forced reranking would regress.
3. Document the focused eval command under `docs/evals/README.md`.

## 2026-06-29 - Candidate-Aware Exact Evidence Protection

Status: implemented

Goal: reduce dependence on finite keyword/regex routing for exact queries by checking whether retrieved candidates contain strong exact tokens from the query.

Plan:

1. Extract strong exact tokens from the query, including email, UUID, phone-like numbers, structured IDs, and mixed alphanumeric tokens with separators.
2. Check fused candidates for exact token evidence in `doc_id`, `chunk_id`, or `content`, with separator normalization.
3. In `auto` mode, skip reranker when exact candidate evidence exists, even if the original route is semantic or hybrid.
4. Add tests for normalized exact evidence and an unrouted exact-token query that old query-only routing would classify as semantic.
