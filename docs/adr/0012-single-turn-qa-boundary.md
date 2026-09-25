# ADR 0012: The question endpoint is single-turn, and says so

## Status

Accepted (2026-09-25)

## Context

`POST /v1/query` accepts `{question}` and keeps no conversation state: no
session, no message list, no history table. Nothing in the code pretends
otherwise. Two questions arrived together and they need separate answers.

**Is single-turn acceptable for an enterprise knowledge base?** Yes.
Single-turn retrieval-and-answer is a legitimate product shape — compliance
lookup, clause retrieval, ticket assistants are all commonly built this way.
"Multi-turn" is a product choice, not an enterprise-grade requirement. An
earlier assessment in this repository listed the absence of multi-turn as a
gap; that was a preference dressed up as a standard and is withdrawn.

**The enterprise requirement that does apply** is narrower: a capability the
system does not have must not be silently simulated. Measured on the deployed
stack with the ordinary-role demo account, four questions that only make sense
with a previous turn:

| question | observed | verdict |
| --- | --- | --- |
| 它的上限是多少？ | `refusal_reason=none`; enumerated every 上限 in the corpus and asked 请确认您具体询问的是哪一项 | correct |
| 那这个呢？ | `insufficient_support`, no sources | correct |
| 刚才那份文档里还写了什么？ | `refusal_reason=none`, answered out of `demo-doc-handbook` | **wrong** |
| 上面说的第二点是什么意思？ | `refusal_reason=none`, answered out of `demo-doc-security` | **wrong** |

The last two are the defect. The citations were faithful and the grounding
verifier passed, because the evidence does support the answer — it just does not
support the *question*. The referent was chosen by the model, and the request
carried nothing that could determine it. No citation check can see this failure:
it lives in the premise.

## Decision

1. **Refuse, do not guess.** A deterministic gate
   (`retrieval.HasUnresolvedReference`) runs before retrieval and returns a
   dedicated reason, `context_required`, whose sentence names the problem and
   the next step. Precision is the priority, because refusing an answerable
   question is worse than the guessing it replaces: the marker list holds whole
   phrases, and lookalike-but-answerable questions are pinned as non-matches
   (刚才上传的文档多久可以查到 / 入职之前需要准备什么材料 /
   上述内容里报销标准是多少 / 员工手册里还写了什么).
2. **Do not add multi-turn now.**
3. **Revisit only against the conditions below**, not against feature parity.

### Why multi-turn is not free

| cost | why it is not free |
| --- | --- |
| Conversation state | a new persisted object needing tenant scoping, TTL and retention — the platform's other durable state (jobs, manifests, sessions) all carries explicit lifecycle governance, and this would have to match |
| **Per-turn permission re-evaluation** | retrieval visibility is computed per query from the actor's role and each document's publish state. A conversation must not become a privilege-laundering channel: turn N's retrieved passages are carried in the context, so they are no longer in the retrieval result set and turn N+1's filter never sees them. Concretely — a caller asks about a `confidential` payroll document they may read, then the document's clearance is raised, or the caller is downgraded to `readonly`, or the document is retired; turn N+1 asks "and the second item?" and, if the carried passage is replayed, is answered from a document the caller may no longer read. "Revoked" means **between two turns** — not a client cancelling a stream mid-answer, which changes nothing about access. What closes this is re-checking every carried passage against the *current* permissions on every turn, which in turn means each passage has to carry enough identity (tenant, document, generation, permission) to be re-checked. This is the only item that makes multi-turn a *security* question rather than a feature gap |
| Citation attribution | a citation must belong to the turn that produced it, so "the answer" stops being a single value and every consumer of it has to change |
| Context management | a truncation policy is needed, plus an answer for what happens when the truncated part *was* the referent |
| Injection surface | a previous turn's content becomes trusted context, so one poisoned document can steer every later turn |
| Evaluation | the eval track is single-turn; multi-turn needs its own gold set, and `safety_refusal_rate` has no meaning for a conversation |

### Conditions under which multi-turn should be added

All three, not any one:

1. A real caller asks for it with a concrete workflow. That is a product
   decision; "other products have it" is not a trigger.
2. The per-turn permission re-evaluation design exists and is tested —
   specifically: revoke access between two turns, and the next turn fails
   closed.
3. A multi-turn evaluation set exists, so "it got worse" is detectable rather
   than argued.

## Consequences

- `context_required` is a new value on the `refusal_reason` wire field. Callers
  that branch on the reason set must add it; the UI now states the single-turn
  boundary in the product instead of leaving the caller to guess why the same
  wording keeps failing.
- The gate runs **before** retrieval, so this refusal carries no retrieval info.
  Deliberate: searching for a referent that is absent returns candidates that
  look like evidence for a question that was never asked.
- Retrieval-only callers are exempt (`RetrievalDiagnosticsEnabled &&
  RetrievalOnly`), so capacity runs and diagnostics can still pass
  marker-bearing questions through to the candidate list.
- **Known boundary:** a bare ordinal back-reference (「第二点是什么意思？」) is
  not detected. It cannot be told apart from an in-document reference
  (「合同第三条是什么？」) without knowing whether a document was named. Missing
  it leaves the old guessing behaviour for that one shape; catching it wrongly
  would refuse an answerable question. A pasted passage that makes 「上述」
  resolvable is not treated as a marker for the same reason.
- `scripts/tests/test_refusal_sentence_contract.py` now pins that every refusal
  sentence the backend can return is recognised by the evaluator, so the next
  reason cannot ship a *correct* refusal that `run-evals.py` scores as a
  hallucinated answer.
- If multi-turn is added later, this gate is where the reference gets *resolved*
  from history instead of refused. The detector is the same either way, so this
  work is not discarded.
