# Backlog

## 2026-06-28 - Enterprise Rerank Policy

Status: implemented, superseded by Full Enterprise Rerank Guardrail

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

Status: implemented, superseded by Full Enterprise Rerank Guardrail

Goal: reduce dependence on finite keyword/regex routing for exact queries by checking whether retrieved candidates contain strong exact tokens from the query.

Plan:

1. Extract strong exact tokens from the query, including email, UUID, phone-like numbers, structured IDs, and mixed alphanumeric tokens with separators.
2. Check fused candidates for exact token evidence in `doc_id`, `chunk_id`, or `content`, with separator normalization.
3. Originally skipped reranker when exact candidate evidence existed; this was superseded by protective rerank + pinning in the full guardrail.
4. Add tests for normalized exact evidence and an unrouted exact-token query that old query-only routing would classify as semantic.

## 2026-06-29 - Full Enterprise Rerank Guardrail

Status: implemented, completed with no-reranker exact pinning

Goal: complete the enterprise rerank design across rule, business schema, retrieval signal, protective rerank, and observability/eval layers.

Plan:

1. Add document/task/chunk `metadata` propagation from upload to parser, Qdrant payload, Elasticsearch document, and retrieval candidates.
2. Add `RETRIEVAL_EXACT_SCHEMA_FIELDS` so configured business fields such as `contract_no`, `trace_id`, and `customer_ref` participate in exact evidence.
3. Add Elasticsearch metadata exact-term `should` clauses so metadata-only identifiers can be recalled even when the content body does not contain the token.
4. Replace candidate-evidence skip with protective rerank in `auto`: semantic/hybrid queries may use reranker, then exact-match candidates are pinned above non-exact candidates.
5. Add structured rerank-decision logs with route, policy, rerank decision, exact token hashes, matched candidate count, top-rank changes, and protection state.
6. Extend deterministic unit tests and golden eval data with metadata-only exact retrieval coverage.
7. Pin exact-evidence candidates even when reranker is disabled or unconfigured, so CI/default deployments keep the same deterministic business-identifier protection.

## 2026-06-29 - No-Reranker Exact Candidate Pinning

Status: implemented

Goal: close the CI/default deployment gap where metadata-only exact candidates could be recalled but still lose fused ranking when no reranker service was configured.

Plan:

1. Reuse the same exact-evidence planner when `RETRIEVAL_ENABLE_RERANK=false` or `RERANK_ENDPOINT` is empty.
2. If semantic/hybrid candidates contain exact evidence, apply `protectExactMatches` directly to fused candidates.
3. Log the decision with reason `reranker_not_configured_exact_candidate_pinned` for observability.
4. Add an engine-level regression test where `customer_ref` metadata is the only exact evidence and the fused top candidate is a distractor.
5. Rerun Go tests and CI-like deterministic eval with reranker disabled.
