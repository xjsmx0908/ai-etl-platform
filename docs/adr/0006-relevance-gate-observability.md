# ADR 0006: Relevance gate is observational, not a hard threshold

## Status
Accepted (2026-08-13)

## Context

Anti-hallucination needs a second line of defense beyond "the LLM refuses when
the prompt tells it to": a hard gate that stops generation when retrieved
evidence is not relevant enough. We evaluated gating on the raw Qdrant cosine
similarity (carried in `Candidate.Relevance`, preserved through RRF fusion).

We re-measured the score distribution on the current semantic corpus
(`docs/evals/semantic-golden-set.json`, 38 positive + 152 negative pairs, real
`nomic-embed-text` 768d embeddings):

| Distribution | min | median | max |
| --- | --- | --- | --- |
| query ↔ target document | 0.5412 | 0.6993 | 0.8987 |
| query ↔ unrelated document | 0.4953 | 0.6044 | **0.8196** |

The two distributions **overlap heavily** (overlap width 0.2784). Any threshold
that removes unrelated candidates also drops real hits:

| threshold | unrelated wrongly kept | real hits wrongly dropped |
| --- | --- | --- |
| 0.58 | 97/152 | 2/38 |
| 0.62 | 64/152 | 5/38 |
| 0.65 | 50/152 | 11/38 |

A hard threshold therefore trades Recall for precision at an unacceptable rate.

## Decision

Do **not** gate generation on a cosine threshold with the current embedding.
Instead:

1. **Expose evidence confidence.** `RetrievalInfo.MaxRelevance` returns the
   highest Qdrant cosine among candidates; the demo UI renders it as a
   high/medium/low indicator. This makes the refusal decision explainable and
   auditable without a brittle numeric gate.
2. **Keep the existing layered defenses.** Permission filtering at retrieval
   source (confidential docs never enter candidates for the user role), plus the
   prompt-level refusal contract (F-01), plus the no-evidence response when
   retrieval yields nothing.
3. **Re-evaluate on embedding change.** The overlap is a property of the
   embedding's semantic resolution, not the code. If we move to a stronger
   Chinese embedding (e.g. bge-m3), re-run this measurement before enabling a
   threshold.

## Consequences

- `RETRIEVAL_MIN_RELEVANCE` remains `0` (gate disabled) and is documented as
  observational, not a quality ceiling.
- The UI surfaces "evidence confidence" so operators can read the score semantics
  themselves rather than trusting an arbitrary hard-coded number.
- Future embedding changes must re-run the distribution check above; a
  clearable separation would justify enabling the gate.
