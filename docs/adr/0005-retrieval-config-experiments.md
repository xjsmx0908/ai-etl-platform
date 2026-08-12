# ADR 0005: Retrieval configuration backed by real-model experiments

## Status
Accepted (2026-08-10)

## Context

Three retrieval choices needed a decision: whether to keep the Elasticsearch BM25
leg, whether the Cross-Encoder reranker helps, and how to evaluate any of this
without fooling ourselves. Early "100% pass" numbers came from an anchor-token
dataset where every query embedded a unique token like `alpha001`; the router
classified all of them as exact-keyword, so the semantic path never ran and the
numbers measured keyword matching, not retrieval quality.

We built a semantic dataset (`docs/evals/semantic-golden-set.json`, 42 cases,
queries paraphrase documents instead of embedding anchor tokens) and ran it with
real models (nomic-embed-text 768d + deepseek-v4-flash). Before trusting any A/B
difference we quantified run-to-run variance: three identical runs produced a
±4.26pp spread in pass_rate, so differences below that are noise.

## Decision

1. **Keep hybrid retrieval (Qdrant dense + Elasticsearch BM25).**
   Disabling ES dropped pass_rate from 69.05% to 52.38% — far above the 4.26%
   noise floor. ES helped 9 cases and hurt 2. Despite ES's weak Chinese
   tokenization, the BM25 leg is a net win for this corpus.

2. **Disable the Cross-Encoder reranker (ms-marco-MiniLM-L6-v2).**
   Disabling it raised pass_rate from 69.05% to 73.81%: it benefited 0 cases and
   hurt 2. The model is English-trained and has no discriminative power over
   Chinese candidates. The exact-match pinning guardrail in `auto` policy is
   model-independent and remains active even with reranking off.

3. **Gate quality claims on the semantic dataset, not the anchor set.**
   The anchor set reports integration-path health only. Quality statements must
   come from the semantic set with real models.

## Consequences

- `RETRIEVAL_ENABLE_RERANK` defaults to on but the experiments recommend turning
  it off until a Chinese reranker (e.g. bge-reranker-v2-m3) is available and
  re-measured. The config decision is data-backed, not a default.
- Every future retrieval experiment must be judged against the 4.26% variance
  floor; smaller differences are not actionable.
- The semantic dataset is the quality gate; the anchor set remains the smoke gate.

## Alternatives considered

**Keep reranker on.** Rejected: negative measured effect on the corpus this
system targets (Chinese enterprise docs). Re-evaluate if the corpus becomes
English or a Chinese reranker lands.

**Trust the anchor-set numbers.** Rejected: they measure token matching. This is
the single most important methodology decision in the project.
