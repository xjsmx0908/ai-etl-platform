# Codex Learnings

This file is an append-only record of completed PRAR cycles.

## 2026-08-29 - P2.4 governance acceptance planning

- **Perceive:** P2.1 publication approval, P2.2 durable document versions, and
  P2.3 generation manifests existed independently. Publication still inspected
  document-level backend counts, approval named only a mutable document id, and
  replacement/deletion lacked one durable release lifecycle.
- **Reason:** Treat PostgreSQL release identity as the publication and query
  correctness seam. Bind assessment and approval to an immutable version,
  generation, digest, and revision; preserve the prior release until an atomic
  approved cutover; make deletion a recoverable workflow.
- **Act:** Proposed ADR 0011 and a five-slice, test-driven P2.4 plan covering the
  release model, exact-candidate approval, replacement/read cutover, recoverable
  deletion, and public-interface governance acceptance.
- **Refine:** An E2E script alone would certify count equality while retaining a
  time-of-check/time-of-use publication hole. The acceptance harness must be
  the final proof over shared production interfaces, not a substitute for the
  missing invariants. No code, deployment, or retention setting changed during
  this planning cycle.

## 2026-08-19 - Legacy Word DOC Parsing

- **Perceive:** A genuine OLE Microsoft Word `.doc` upload repeatedly failed in parser-service with `PackageNotFoundError` and entered the Kafka DLQ.
- **Reason:** The upload and worker layers accepted `.doc`, but parser-service routed it to `python-docx`, which only reads OOXML packages. The runtime image had no legacy Word converter.
- **Act:** Added a dedicated parser that runs headless LibreOffice without a shell, inside a per-request temporary profile, with a 60-second timeout. Kept DOCX parsing unchanged and preserved the original upload size.
- **Refine:** Added a public-boundary regression test, rebuilt the production image, ran all parser tests, and replayed the original object through the authenticated parser API. The request succeeded with 18 chunks.
- **Prevention:** Every advertised file extension needs a fixture or boundary test that exercises its real on-disk format; sharing a parser based only on similar extensions is insufficient.

## 2026-08-19 - Document Content Search Web Proxy

- **Perceive:** Content searches from the Web document manager returned `q is required`, while the same `q` request sent directly to Query API succeeded.
- **Reason:** The client generated `/api/documents/search?q=...`, but no matching static Next.js route existed. The request fell through to `/api/documents/[id]`, where `search` became a document id and the query string was dropped.
- **Act:** Added a dedicated search proxy that forwards the complete query string and the HttpOnly session token. Extended the isolated smoke stack with a Web port and a session-proxy content-search assertion.
- **Refine:** Rebuilt and redeployed Web, verified the reported Chinese query returned HTTP 200, and passed the production build, TypeScript check, service tests, Trivy/configuration gates, and full pipeline smoke. `npm audit` retained the existing Next.js/PostCSS findings, and the deterministic eval runner remained blocked by an existing Compose compatibility issue where an unpublished port resolves to `0`.
- **Prevention:** Every client API path needs a matching proxy-route test at the browser-facing boundary; backend endpoint coverage alone cannot detect Next.js route fallthrough.

## 2026-08-19 - RAG Evidence Integrity and Scope Isolation

- **Perceive:** `办公用品` retrieved repeated chunks from one legacy Word file,
  mixed a user upload with demo policy, showed an RRF rank score as cosine, and
  called every retrieved chunk a citation. SSE also bypassed JSON-path safety.
- **Reason:** Deduplication stopped at chunk ID, final Top-K had no document or
  file diversity, Chinese `standard` analysis used broad single-character OR,
  knowledge scope was absent from retrieval, and the response contract did not
  distinguish model evidence from cited sources.
- **Act:** Added parser and retrieval content deduplication, file-hash collapse,
  strict document diversity, stable raw relevance, JSON/SSE path unification,
  phrase-first CJK search, versioned index migration, scope filtering, citation
  extraction, truthful stage counts, and matching Workbench controls.
- **Refine:** Rebuilt the live services, migrated 71 ES documents to a CJK alias,
  reprocessed both legacy Word copies, backfilled demo scope, flushed semantic
  cache, and verified document search and SSE with a normal-user token.
- **Prevention:** Retrieval diagnostics must retain stage-specific values; source
  identity needs content and file semantics in addition to IDs; any knowledge
  space added to reads also needs an explicit write authorization boundary.

## 2026-08-19 - Enterprise Knowledge Governance P0

- **Perceive:** Free-form scope metadata and post-retrieval Top-1 selection could
  mix policies, hide authorization decisions, and allow draft or demo evidence
  to reach generation.
- **Reason:** Enterprise isolation needs an authoritative catalog resolved before
  retrieval, explicit publication state, and fail-closed evidence validation.
- **Act:** Added tenant-scoped spaces and membership roles, upload/query ACLs,
  draft/published/retired lifecycle controls, catalog-backed evidence filtering,
  management APIs, and matching Web controls and diagnostics.
- **Refine:** A fresh-stack E2E exposed that tenants created after migration had
  no default space. Added migration `0006` to backfill and transactionally
  provision new tenants. The eval runner also gained a loopback random port and
  role-correct fixture uploads. Full suites, E2E, live checks, and the 47-case
  deterministic eval then passed their configured gates.
- **Prevention:** Any trigger that inserts a child row must have the parent
  invariant established for both migrated and newly created owners. Evaluation
  fixtures must enter through real identities and authorization paths rather
  than synthetic JWTs that bypass catalog membership.

## 2026-08-19 - Evidence Sufficiency and Safe Refusal

- **Perceive:** Permission filtering correctly removed confidential targets, but
  unrelated same-tenant candidates still reached the mock LLM. Its refusal
  variant retained unrelated sources, so three security negatives failed answer
  assertions despite passing retrieval isolation.
- **Reason:** Similarity and rank cannot prove a requested contract, order, or
  trace identifier exists. That proof must be deterministic and applied after
  every authorization and lifecycle filter, before generation.
- **Act:** Reused the retrieval engine's strong-token normalization to add an
  exact-evidence sufficiency gate, normalized refusal variants, removed all
  refusal sources, exposed boolean diagnostics in the Workbench, and added an
  independent 100% negative-case CI gate.
- **Refine:** Public HTTP and retrieval tests passed, followed by the 47-case
  isolated eval at 100% retrieval, answer, overall, and negative pass rates.
  Every confidential negative returned the fixed refusal with zero sources.
- **Prevention:** Safety cohorts require their own zero-tolerance metric; an
  aggregate quality score must never hide a permission or evidence failure.
- **Deployment verification:** The rebuilt live API refused an unknown contract
  id with zero retrieved sources and citations, while `办公用品` still returned
  grounded evidence from one document. Boolean audit diagnostics now serialize
  explicit `false` values; omitting them makes "failed" indistinguishable from
  "instrumentation absent" to API consumers.

## 2026-08-19 - Web Framework Security Upgrade

- **Perceive:** Next.js 14 and its transitive PostCSS dependency had high-severity
  advisories. The initially evaluated Next.js 15 backport still selected a Sharp
  release covered by newer high-severity libvips advisories.
- **Reason:** Overriding Sharp outside Next.js 15's declared range would produce
  an unsupported combination. Next.js 16.3.1 is the first supported dependency
  set that clears Next.js, PostCSS, and Sharp findings together and requires
  Node 20 plus async dynamic-route parameters.
- **Act:** Upgraded Next.js and the container runtime, migrated route and viewport
  contracts, established ESLint 9, regenerated the official-registry lock file,
  and excluded dependencies, build output, npm config, and environment files
  from the Docker build context.
- **Refine:** Clean install, full audit, lint, TypeScript, production build,
  no-cache image build, isolated end-to-end smoke, and deployed session-proxy
  checks all passed. `npm audit` reports zero vulnerabilities.
- **Prevention:** A dependency gate must inspect transitive native packages, not
  only the named framework. Docker contexts must explicitly exclude local env
  and package-manager files before any image is considered releasable.

## 2026-08-19 - Public RAG Retrieval Baseline

- **Perceive:** Public RAG datasets can provide a reproducible generic retrieval
  baseline, but they do not represent this product's Chinese enterprise policies,
  authorization rules, or answer-quality acceptance criteria.
- **Reason:** A benchmark needs immutable provenance and a document/query data
  model. The legacy case-per-document format duplicated shared corpus documents,
  lost multi-relevance qrels, and implicitly required answers for retrieval-only
  datasets.
- **Act:** Added evaluation protocol v2, a licensed BEIR importer, an approved
  NanoSciFact catalog, and a paginated Dataset Server downloader pinned by commit,
  expected row counts, and SHA-256 hashes. Reports now disclose source, license,
  scope, sampling comparability, and model mode.
- **Refine:** Imported and validated 2,919 documents, 50 queries, and 56 qrels;
  passed 38 script tests, a 30-document v2 isolated smoke, and the legacy 47-case
  regression. Public corpus and reports remain Git-ignored.
- **Prevention:** Never treat mock embedding scores, sampled public results, or
  an English scientific benchmark as enterprise acceptance evidence. Public
  baselines complement rather than replace reviewed, deidentified business data.

## 2026-08-19 - Real Model Baseline and ETL Readiness

- **Perceive:** The configured stack had usable local `bge-m3` embeddings and an
  authenticated external `deepseek-v4-flash` endpoint, but no local chat model.
- **Reason:** A real retrieval result is meaningful only when the model mode,
  fixed dataset revision, sampling, reranker policy, token usage, and cost limits
  are disclosed. ES count alone cannot prove that all uploaded documents have
  completed ETL.
- **Act:** Ran a matched 30-document/10-query sample. Recall@5 was 70% without
  reranking and 90% with `auto` reranking. Added task-status polling before
  publication so large uploads cannot fail with a 409 lifecycle race.
- **Refine:** Both isolated runs cleaned their Compose projects; reports retain
  per-query misses and token counts. A 300-document attempt failed before scoring
  because ETL was incomplete, which motivated the readiness fix rather than being
  recorded as a model result.
- **Prevention:** Full public baselines against a billed endpoint require an
  explicit budget and time window. Sampled scores are directional only and must
  not become enterprise quality gates.

## 2026-08-20 - Full Real Retrieval Baseline

- **Perceive:** The complete NanoSciFact snapshot contained 2,919 documents and
  50 queries. Local F16 `bge-m3` embedding on the four-thread host made ETL
  readiness much slower than the per-query timeout used by the evaluator.
- **Reason:** ES count is only an indexing signal; publication must wait on every
  task status. A reranker comparison must also use the exact same published
  corpus, otherwise upload timing and vectorization become confounders.
- **Act:** Added a separate batch `--processing-timeout`, stopped a readiness
  scan immediately on tenant-wide 429s, and added a dataset-digest-bound upload
  map for corpus reuse. Completed matched real-model runs with and without
  `auto` reranking.
- **Refine:** No-rerank Recall@1/3/5 was 52%/60%/66%; rerank was 54%/72%/72%.
  Reranking recovered 4 misses, regressed 1 hit, and raised tokens 14.1%.
  Answer and negative assertions were 100% in this retrieval-only dataset.
- **Prevention:** Keep public benchmark scores separate from enterprise gates;
  collect reviewed Chinese business qrels and repeat the comparison before
  selecting production defaults.

## 2026-08-20 - Enterprise Chinese Auto-Silver Baseline

- **Perceive:** All 40 private documents could be extracted locally, but the
  corpus included confidential candidates, low-content files, PII indicators,
  and a possible version conflict. Treating model-written questions as gold
  would have hidden both data-governance uncertainty and evaluator noise.
- **Reason:** Silver generation must bind every answer to verbatim evidence and
  keep private artifacts local. Retrieval, citation, answer content, refusal,
  and permission behavior require separate metrics because one aggregate score
  cannot locate the failing layer.
- **Act:** Added a local evidence-first dataset builder and review tables, binary
  source uploads with digest checks, query-aware context selection, configurable
  local LLM limits, and an isolated real-model runner. Generated 114 cases and
  evaluated them with local `bge-m3` plus `qwen2.5:1.5b` without reranking.
- **Refine:** Recall@5 reached 83.49% and all 5 no-answer cases passed, while
  answer assertions reached only 26.32%. Failure aggregation found 18 retrieval
  misses, 27 missing citations after retrieval, and 41 missing key facts after
  retrieval. Validation also showed high query/evidence lexical overlap, so the
  retrieval score may overstate natural-query performance. Private case details
  remained Git-ignored and the eval stack, volumes, networks, and images were
  removed after the run.
- **Prevention:** Do not promote auto-silver results to release gates. Require
  data-owner review of permissions, effective versions, conflicts, and uncertain
  labels; then repeat matched reranker and answer-quality experiments with a
  grounding verifier and multiple runs.

## 2026-08-20 - Automated Enterprise Review and Technical Gold Draft

- **Perceive:** Mechanical evidence checks passed all 111 positive candidates,
  but that did not make their questions natural or answers complete. Requiring
  the user to review every row would waste attention, while letting a local
  model declare business validity would create false authority.
- **Reason:** Technical and authority decisions need separate outputs. Source
  integrity, conservative permissions, evidence binding, and semantic label
  quality can be automated; current policy effectiveness, official ownership,
  and the winning version in a conflict cannot be inferred as fact.
- **Act:** Added a cached local review tool with tested permission, quality,
  conflict, evidence, and promotion rules. It emits audit CSVs, a technical gold
  draft, and a minimal authority-only confirmation list while preserving the
  original review tables.
- **Refine:** Twenty-seven documents and 20 cases entered the draft; 4 documents
  were excluded for low content, 2 were held for a version conflict, and 7 need
  effectiveness confirmation. Only 16/111 positives passed independent semantic
  review, while all 17 safety negatives passed. The remaining manual list has 8
  items and all 71 script tests passed.
- **Prevention:** Lexical-overlap warnings must be cohort labels rather than a
  universal rejection rule: exact identifiers and policy terms are legitimate
  enterprise queries, but their scores cannot prove semantic retrieval quality.
  Never call a technical draft business-approved gold.

## 2026-08-21 - Latest-Date Confirmation Rule

- **Perceive:** The remaining 8 confirmations contained 7 dated historical
  items and one duplicate-version pair. Content dates tied for the duplicate,
  while embedded Office modification metadata differed.
- **Reason:** A reproducible recency rule needs an evidence hierarchy and must
  fail on ties. Filesystem upload timestamps are not business dates; content,
  filename, PDF metadata, and Office metadata are auditable inputs, but still do
  not equal policy-register validation.
- **Act:** Added a tested confirmation applicator that extracts full dates,
  compares embedded metadata, records source and basis, rejects unresolved
  ties, excludes the losing version, and builds a private Gold candidate.
- **Refine:** All 8 rows resolved under the user-approved rule. The candidate
  contains 35 documents and 31 cases with no broken references; 75 script tests
  and strict dataset validation passed. Only one positive case is currently in
  the low-overlap semantic cohort.
- **Prevention:** Preserve `business_approval_complete=false` for recency-based
  confirmation. Report lexical, semantic, and safety cohorts separately, and
  prioritize natural semantic and cross-document case creation next.

## 2026-08-21 - Semantic and Cross-Document Eval Expansion

- **Perceive:** The first reviewed candidate had only one semantic case and no
  cross-document cases. Whole-document Chinese character overlap also falsely
  classified natural questions against long policies as lexical copies.
- **Reason:** Natural-query quality must be measured against bound evidence,
  while cross-document acceptance must require every source in retrieval and
  citations. Model output cannot replace deterministic evidence constraints.
- **Act:** Added a local-only, cached generation and independent-review pipeline,
  evidence-relative overlap checks, deterministic cross-document answer
  composition, explicit cohorts, and `required_doc_ids` evaluator semantics.
- **Refine:** Generated a 35-document/70-case technical candidate with 30
  semantic, 15 lexical, 10 cross-document, and 15 safety-negative cases. All 40
  positive semantic/cross cases have verbatim bindings; source hashes and final
  integrity checks reported zero errors.
- **Prevention:** Fail closed on missing local review, unsupported facts,
  incompatible scopes, missing sources, or non-loopback model endpoints. Keep
  this candidate distinct from business-approved Gold and compare rerankers by
  cohort over repeated matched runs.

## 2026-08-22 - Enterprise Reranker Repeated A/B Evaluation

- **Perceive:** The first diagnostic run looked like poor retrieval, but 40 of
  70 answers were empty because a hard-coded 60-second HTTP middleware timeout
  cancelled the local LLM before its configured 180-second timeout. Treating
  those missing responses as retrieval misses would have published invalid
  quality evidence.
- **Reason:** A repeated comparison needs both configuration matching and a run
  completeness gate. Cohort means are insufficient without within-arm spread,
  and noise from latency milliseconds must never be combined numerically with
  percentage metrics.
- **Act:** Added a configurable handler timeout with a backward-compatible
  default, evaluator provenance and completeness fields, cohort summaries, and
  a matched 3+3 analyzer that rejects invalid or drifting reports. Ran all six
  measurements against one private dataset digest and upload map using only
  loopback Ollama models.
- **Refine:** All six reports had 70/70 successful Query API cases and complete
  grounding. `auto` reduced semantic Recall@5 from 68.89% to 33.33%, lexical
  Recall@5 from 66.67% to 35.56%, left cross-document all-source recall at 0%,
  and failed the 100% safety-refusal rule. It therefore did not pass the release
  gate despite lower observed latency and token use in several cohorts.
- **Prevention:** Gate evaluator validity before reading quality scores, keep
  per-metric noise in its native unit, and do not select a reranker from a public
  English model name or latency result alone. Cross-document zero recall must be
  fixed in candidate generation/fusion before reranking can be credited.

## 2026-08-23 - P1.8 Stage Diagnostics and Environment Boundaries

- **Perceive:** The P1.7 result showed 0% cross-document all-source recall in both
  arms, but the API exposed only final sources and total counts. The possible
  loss points were backend Top-50, Qdrant's internal RRF, application fusion, or
  final diversity selection.
- **Reason:** A diagnostic protocol must identify stage coverage without exposing
  private case identifiers or evidence. It also must bypass semantic cache so a
  cached result cannot hide backend-stage observations.
- **Act:** Added aggregate-only `StageDiagnostics`, controlled by the disabled-by-
  default `RETRIEVAL_DIAGNOSTICS_ENABLED` flag, with deterministic unit and engine
  tests. The evaluator now sends required ids only to an enabled local service and
  reports backend/fused/selected all-required rates. Made isolated eval user
  provisioning idempotent and lowered only the eval Compose ES disk thresholds.
- **Refine:** Focused Go and evaluator tests pass. Two real diagnostic attempts
  were excluded: one reused document ids without their project volumes and hit
  empty ES shards; the fresh isolated attempt hit host disk pressure and then a
  local Ollama embedding circuit breaker. No production retrieval repair was
  inferred from those invalid runs.
- **Prevention:** Treat upload mappings as identity metadata, not portable data
  volumes. Require `run_valid=true` before stage conclusions, and keep reranker
  changes blocked until a valid diagnostic run identifies the upstream loss.

## 2026-08-23 - P1.8 Cohort-Scoped Retrieval Diagnostics

- **Perceive:** A full 70-case answer evaluation was too expensive and coupled
  retrieval diagnosis to local LLM latency, while cross-document cases still
  needed all 35 documents present as distractors.
- **Reason:** The evaluator must narrow only the query cohort, never the upload
  corpus. A retrieval-only request must also be explicitly feature-gated so a
  client cannot silently suppress answer generation in the normal API.
- **Act:** Added `--cohort`, `--retrieval-only`, Query Service gating, config
  coverage, and tests. Stage Markdown now reports Qdrant and Elasticsearch
  independently; provenance records selected cohort and retrieval-only mode.
- **Refine:** Python evaluator tests (33), Go full tests, Go vet, compilation,
  and diff checks pass. The root stack stayed running. Resource preflight found
  5.9GB free on a 97%-full root filesystem; one loopback `bge-m3` probe passed,
  so no invalid full diagnostic run was started.
- **Prevention:** Keep the 10-case diagnostic loop retrieval-only until a valid
  stage boundary is identified, and treat disk headroom plus repeated embedding
  probes as hard preconditions for isolated Compose evaluation.

## 2026-08-23 - P1.8 Ranking Diagnosis After Valid Runs

- **Perceive:** A valid retrieval-only run showed that some multi-source loss
  occurs before final selection, while other cases have every necessary source
  after fusion but lose one before Top-5. Elasticsearch successfully returned
  an empty candidate set, but the engine omitted successful empty backends from
  diagnostics.
- **Reason:** A hit boolean cannot distinguish rank 6 from rank 50, and changing
  diversity without that information confounds diagnosis with product behavior.
  The safe next signal is the deepest first-occurrence rank only when all
  necessary sources are present.
- **Act:** Retained successful empty backend results, tested a distinct-document-
  first selector in an isolated run, rejected and reverted it after no metric
  improved, and added aggregate-only `all_required_max_rank` with test-first
  coverage and JSON privacy assertions.
- **Refine:** Focused retrieval tests, 102 scripts tests, full Go tests, Go vet,
  Compose configuration validation, and diff checks pass. The evaluator already
  preserves stage diagnostic objects verbatim in ignored detailed reports, so
  no private rank data was added to tracked aggregate reports.
- **Prevention:** Require a valid real diagnostic before retaining ranking
  changes. Reuse one isolated corpus and rebuild only Query API for subsequent
  hypotheses; never infer that document diversity is missing merely from a
  multi-source Top-K failure.

## 2026-08-23 - P1.8 Ingestion Completeness and Deep Semantic Rank

- **Perceive:** Anonymous rank diagnostics showed complete necessary sources at
  fused ranks 8-45 for 40% of cases. K=200 raised backend completeness to 80%
  but placed new sources as deep as rank 187. A full-collection dense query still
  missed one case because two completed registry documents had no Qdrant points.
- **Reason:** Retrieval quality cannot be judged against a corpus that reports
  successful ingestion while storing zero chunks. Keyword-based noise filters
  must fail conservatively: a long body mentioning a directory is not itself a
  table of contents. Candidate depth can reveal an upper bound but cannot make a
  rank-216 source fit into Top-5.
- **Act:** Added a pipeline error for zero stored chunks, narrowed TOC filtering
  to short fragments, and added regression tests. Reprocessed only the two empty
  documents; Qdrant then covered all 35 documents and every necessary source was
  present in the full collection.
- **Refine:** After ingestion repair, dense full-collection recall was 10/10,
  while production K=50 fused coverage remained 40% and final Top-5 remained 0%.
  Sparse unigram/bigram, MMR, distinct-document selection, and local multi-query
  candidates failed or regressed and were reverted or left unimplemented. Full
  verification passed: Go tests and vet, 32 parser tests, one reranker test, 102
  script tests, Web lint/typecheck/build, Compose validation, and diff checks;
  all 17 root Compose services remained running.
- **Prevention:** Gate eval validity on non-zero chunks for successfully ingested
  non-empty documents. Preserve only hypotheses that improve a valid real run,
  and distinguish corpus completeness, candidate recall, and final ranking as
  separate release gates.

## 2026-08-24 - P1.8-B Deterministic Clause Retrieval Rejection

- **Perceive:** Full-collection dense retrieval contained every necessary source,
  but some appeared as deep as rank 216. A deterministic clause candidate could
  promote explicit semantic facets without using evaluation-required ids.
- **Reason:** The external retrieval interface should stay small; clause planning,
  governed dense search, fusion, and fallback belong inside the retrieval module.
  Activation must be observable because a non-triggering strategy cannot be
  credited or rejected from final recall alone.
- **Act:** Built the candidate test-first behind a disabled switch, verified its
  public-safe promotion, authorization, exact-query, timeout, determinism, and
  provenance behavior, then ran one matched feature-off/on isolated comparison.
- **Refine:** Both runs were valid with 10/10 successful retrieval-only requests.
  The candidate expanded 0/10 questions; Qdrant/Fuse completeness stayed 40%
  and final Top-5 stayed 0%. The questions carried implicit cross-document intent
  rather than two safely separable clauses, so all candidate code and settings
  were reverted. Dedicated containers, volumes, network, and images were removed.
- **Prevention:** Measure strategy activation separately from outcome. Do not
  loosen deterministic string splitting until it guesses latent intent; move the
  next design toward document-side semantic units or a constrained local planner,
  with a public-safe activation benchmark before private evaluation.

## 2026-08-24 - P1.8-C Constrained Local Query Planner Rejection

- **Perceive:** Implicit cross-document questions need latent facet inference,
  but a planner can also expand ordinary semantic questions or exact lookups and
  silently damage retrieval precision.
- **Reason:** Planner capability must be proven on public-safe activation and
  facet-coverage controls before it sees private evaluation data. Strict output,
  local-only access, governed scope propagation, deterministic fusion, and safe
  fallback are necessary but do not compensate for a model that fails the
  behavioral gate.
- **Act:** Added an 18-case synthetic benchmark and parser/gate tests. Built the
  runtime candidate test-first, then screened local `qwen2.5:1.5b` and
  `qwen3:4b` models using aggregate-only reports.
- **Refine:** The corrected 1.5B prompt over-activated all cohorts and covered
  only 6.25% of expected facets. The 4B model produced 0% valid responses in its
  one-run screen. The formal three-run and private gates were not started, and
  all runtime/configuration candidate behavior was reverted. Complete Go,
  Python, scripts, Web, Compose, and residual verification then passed.
- **Prevention:** Separate interface correctness from model capability, screen
  cheaply before repeated or private evaluation, and retain no ranking behavior
  when the predeclared public gate fails. Future work should improve the public
  planner capability or evaluate document-side semantic units first.

## 2026-08-24 - P1.9 Business Gold Gate and Responsive Refusal

- **Perceive:** P1.7 permission-negative failures did not retrieve forbidden
  documents. The model instead answered from permitted but tangential evidence,
  and the existing verifier accepted that answer because it checked support but
  not whether the response answered the user's question. Separately, the private
  technical candidate still lacked an authorized business approval artifact.
- **Reason:** Evidence support and question responsiveness are independent
  conditions. The answer boundary must require both, while Gold promotion must
  bind an exact candidate and complete approval scope to independently retained
  signed evidence rather than treating technical review as business authority.
- **Act:** Extended ambiguous-band answer verification with the required
  `answers_question` verdict and a public grounded/non-responsive regression.
  Added a fail-closed Gold finalization CLI, synthetic tests, and an approval
  template with candidate and signed-artifact digests, full ID scope, approver
  metadata, and four mandatory attestations.
- **Refine:** Focused and full engineering verification passed. One real-model
  screen and three configuration-matched formal safety runs were all valid, each
  completing 15/15 queries with zero unavailable grounding checks and 100%
  safety refusal. The isolated evaluation project and its dedicated storage and
  images were removed afterward; the root development stack stayed healthy.
- **Prevention:** Do not equate grounded text with a responsive answer, and do
  not infer business approval from automated or technical checks. Keep detailed
  enterprise cases private, require three exact-gate repetitions for safety, and
  leave promotion blocked until an authorized signed artifact is present.

## 2026-08-25 - Completed Upload Publication and Elasticsearch Recovery

- **Perceive:** The reported upload had completed ETL and all vector chunks were
  present, but governance excluded it because its registry state remained
  `draft`. Elasticsearch had also crossed its flood-stage threshold, applied a
  read-only block, and initially accepted none of the document's text chunks.
- **Reason:** Searchability requires both publication eligibility and healthy
  indexes. The narrow product rule is to auto-publish only completed documents
  in the default user upload space; other spaces must retain manual publication
  control. A local single-node search service also needs absolute watermarks that
  reflect the host's intentionally small remaining disk budget.
- **Act:** Added the conditional publication transition to the existing status
  update, covered the SQL contract test-first, configured overridable absolute
  disk watermarks, cleared the read-only block, and replayed only the affected
  retry records. Rebuilt and redeployed only the worker.
- **Refine:** PostgreSQL reports the upload as completed and published; Qdrant
  and Elasticsearch each contain all 23 chunks; retry and dead-letter queues are
  empty; and a real governed query returns the target among five sources with no
  unpublished filtering. Full Go tests, race coverage, vet, formatting, root and
  eval Compose validation, API readiness, stack health, and diff checks passed.
- **Prevention:** Diagnose “not found” across registry governance, vector index,
  text index, and queue state before changing retrieval. Publish only at the
  completed transition, and prefer explicit service watermarks over repeatedly
  clearing a disk-protection block without fixing its trigger.

## 2026-08-25 - Public HTTPS Domain

- **Perceive:** The domain was registered at one provider while its authoritative
  nameservers were hosted by another. Records added only at the registrar were
  invisible to public resolvers. The host already ran Nginx for another site and
  the RAG Web service was healthy on port 3100.
- **Reason:** Registrar ownership and authoritative DNS hosting are separate.
  The lowest-risk deployment adds one isolated Nginx virtual host, keeps the
  application and existing sites unchanged, and enables Secure cookies only
  after HTTPS is operational.
- **Act:** Added the authoritative DNS record, deployed a versioned Nginx proxy,
  issued a Let's Encrypt certificate, enabled HTTP-to-HTTPS redirection, set the
  local Web deployment to `COOKIE_SECURE=true`, and recreated only the Web
  container.
- **Refine:** Public HTTPS homepage and login return 200, unauthenticated
  protected access returns 401, the origin presents the expected certificate,
  HTTP redirects to HTTPS, oversized uploads return 413 before transfer, and a
  Certbot renewal dry run succeeds. The existing blog remains reachable and all
  17 root services remain running with no unhealthy service.
- **Prevention:** Query authoritative nameservers before changing DNS, keep
  per-domain proxy configuration versioned, and test certificate renewal plus
  application cookie policy as part of HTTPS delivery.

## 2026-08-25 - P2.1 Document Publication Governance Agent

- **Perceive:** The Agent page duplicated normal RAG question answering, while
  managed drafts could still be published directly without deterministic index
  checks or requester/approver separation.
- **Reason:** Publication policy belongs in one deep business module and a
  deterministic workflow. An LLM may assist elsewhere, but must not choose,
  skip, or reorder readiness and approval gates for a side effect.
- **Act:** Added managed-document readiness assessment, exact Qdrant/Elasticsearch
  counts, PostgreSQL approvals, four-eyes enforcement, idempotent audited
  publication, planner routing, browser proxy actions, and a governance
  workbench that exposes checks and actions without chain-of-thought.
- **Refine:** Module and HTTP tests cover blockers, approval, self-approval,
  replay, and index failures. Full Go tests, Race/coverage, vet, Web
  lint/typecheck/build, Compose validation, and diff checks pass. Live managed
  drafts verified both deterministic blockers and the ready/pending path; a
  self-approval returned 403, then cancellation left the document as draft.
- **Prevention:** Keep `user-uploads` completion publication independent from
  managed governance, reject direct managed publication, and fail closed when
  either search index cannot prove document completeness.

## 2026-08-25 - P2.1 Acceptance and Eval Publication Compatibility

- **Perceive:** A live two-administrator acceptance completed publication, but
  the deterministic CI job failed after ETL because its legacy helper issued a
  direct `published` PATCH for every evaluation document. Those fixtures enter
  `user-uploads` and were already published by the completed-status transition.
- **Reason:** Managed publication must continue through the Agent, while default
  uploads must not be pushed through that workflow or redundantly republished.
  The evaluator only needs to assert the state established by production logic.
- **Act:** Completed the four-eyes live acceptance, repaired one stale synthetic
  vector payload that predated knowledge-space metadata, and replaced the eval
  publication mutation with a tenant-scoped document GET assertion. Added
  positive and fail-closed regression tests.
- **Refine:** All 113 script tests and Python syntax checks passed. The exact CI
  deterministic evaluation completed 47/47 documents and queries with 100% hit,
  pass, answer, negative, acceptable-hit, and Recall@5 rates; its isolated images
  were removed and the 17-service root stack remained healthy.
- **Prevention:** When a lifecycle transition gains an automatic side effect,
  audit operational scripts for duplicate mutations. Prefer verifying the
  resulting state over replaying a transition, and keep managed and default
  publication paths explicit in tests.

## 2026-08-27 - Enterprise RAG Design Acceptance and Implementation Baseline

- **Perceive:** The accepted enterprise design identified production gaps, but
  the current upload and indexing implementations needed to be read before
  converting recommendations into work. Upload currently commits object,
  Redis, Kafka, and PostgreSQL in separate steps; indexing treats Qdrant as
  primary and Elasticsearch as a retryable projection.
- **Reason:** Implementation should start only after the real failure seams are
  explicit. PostgreSQL must own admission and projection manifests, while Kafka,
  Qdrant, Elasticsearch, and Redis remain delivery or derived-state adapters.
  Identity and SLO values require business inputs and cannot be invented by
  engineering.
- **Act:** Accepted and merged the reviewed enterprise design, then proposed
  ADRs for transactional outbox admission, generation-scoped index manifests,
  enterprise identity federation, and production SLO/recovery gates. Added a
  staged backlog with explicit external inputs and acceptance criteria.
- **Refine:** Kept all four ADRs Proposed, recorded unresolved decisions instead
  of silently choosing providers or targets, and tied every phase to tests and
  evidence. Documentation verification and PR review remain required before any
  implementation begins.
- **Prevention:** Read the actual write order and failure handling before
  proposing distributed consistency changes. Treat equal index counts as
  insufficient without identity manifests, and never label healthy development
  containers as production readiness evidence.

## 2026-08-27 - P2.2 durable admission, first vertical slice

- Perceive: the upload gateway made Kafka durable before the authoritative
  PostgreSQL catalog, so a successful enqueue could permanently lose ownership
  and governance facts; Kafka failure could strand an object.
- Reason: keep the current API and worker payload compatible, but move the
  acceptance boundary to one PostgreSQL transaction containing document, job,
  and outbox state. Use immutable object keys because S3 and PostgreSQL cannot
  share a transaction.
- Act: added stable job/event identities, transactional admission, leased
  `SKIP LOCKED` outbox relay claims, retry recovery, exact cleanup on admission
  failure, and behavior/transaction/migration tests.
- Refine: deterministic identities must include tenant and idempotency key, and
  immutable keys must also include the content digest so a conflicting retry
  cannot overwrite a previously accepted object before conflict detection.

## 2026-08-27 - P2.2 consumer idempotency and terminal state

- Perceive: at-least-once outbox publication supplies stable event/job IDs, but
  the worker previously had no durable claim or terminal record and concurrent
  workers could commit a later partition offset past an earlier NACK.
- Reason: deduplication requires a lease, not a permanent "seen" flag. Job and
  document terminal state must commit together before Kafka ACK; partition
  commits must advance only across the fetched prefix that reached terminal
  handling.
- Act: added processing leases, terminal compare-and-set transitions, duplicate
  ACK/busy retry behavior, local NACK redelivery, and ordered partition commit
  tracking that also supports compacted offset gaps.
- Refine: relay publication state is a one-way transition and must never regress
  a fast worker's processing or terminal state back to published. Lease duration
  must exceed the configured worst-case pipeline retry window.

## 2026-08-27 - P2.2 deployed lease configuration regression

- Perceive: PR CI accepted every unit suite but the deterministic evaluation
  uploaded 47 documents, indexed none in Elasticsearch, and timed out while all
  tasks remained pending.
- Reason: application defaults validated in Go used a five-minute pipeline
  timeout, while Compose deployed a 15-minute timeout with three retries. Its
  roughly 60-minute worst-case window exceeded the new 30-minute job lease, so
  the worker rejected its configuration before consuming Kafka.
- Act: added a deployment-boundary regression test and set the Compose and
  example ingestion lease to 75 minutes, preserving recovery while covering the
  configured retry window with scheduling margin.
- Refine: configuration validation tests must exercise deployed combinations,
  not only application defaults. Compose syntax validation cannot prove that a
  service accepts the resulting environment.

## 2026-08-27 - P2.2 ingestion operations and crash recovery

- Perceive: durable admission and idempotent consumption still lacked signals
  for stuck work, bounded cleanup for the object/PostgreSQL gap, and acceptance
  evidence across a relay/API crash after HTTP `202`.
- Reason: operational metrics must come from authoritative PostgreSQL state and
  avoid tenant/document labels. Cleanup must fail closed, scan fairly, and
  protect every object owned by an admitted job—including terminal history—until
  an explicit generation-retention policy exists.
- Act: added snapshot gauges and sustained alert rules, an indexed catalog/job
  reference check, fair bounded MinIO collection, configuration boundaries, and
  an isolated crash E2E that interrupts Kafka, worker, and Query API progress.
- Refine: a batch limit applied before candidate filtering can starve cleanup;
  a current-document-only reference check can delete queued or historical
  admitted versions. Keep object candidates version-scoped, retain a scan
  cursor, and treat database uncertainty as a no-delete result.
- Verification: Go race tests, full Go tests and vet, 117 script tests, parser
  and reranker pytest suites, Compose overlays, Prometheus config/rules, and the
  real isolated crash-recovery flow passed. Generation activation remains a
  separate ADR 0008 phase and is not claimed by this slice.

## 2026-08-28 - P2.3 generation manifest persistence

- Perceive: Qdrant success plus asynchronous Elasticsearch enqueue cannot prove
  that two derived projections contain the same chunks, and unordered worker
  completion makes insertion order unsuitable as an identity.
- Reason: place lifecycle correctness behind one manifest module interface.
  Bind a sorted digest to the generation and document version, and keep
  readiness and activation predicates inside PostgreSQL compare-and-set writes.
- Act: added the manifest migration, deterministic digest, state validation,
  backend observations, failure recording, and serialized transactional
  activation with a database-enforced single-active invariant.
- Refine: activation must retire and promote in one transaction because the
  partial unique index prevents promotion while another generation is active.
  Checking the promoted row count before commit ensures a missing/non-ready
  target rolls back retirement, leaving the old generation queryable.
- Scope: this slice establishes persistence only. Pipeline completion semantics,
  backend verification, reconciliation, cleanup, and operations evidence remain
  explicit P2.3 work and are not claimed complete.
- Review refinement: serialization alone is not stale-writer protection. An
  activation must compare the caller's expected current generation after taking
  the lock. At-least-once delivery also makes idempotent manifest creation and a
  controlled failed-to-building retry transition prerequisites for pipeline
  integration. Durable ingestion `job_id` is the authoritative document-version
  key, enforced together with tenant/document identity by a composite foreign
  key. Mock SQL tests were supplemented by a disposable-schema PostgreSQL race.

## 2026-08-28 - P2.3 generation-aware backend projections

- Perceive: a durable manifest is useful only if backend observations can be
  scoped to one generation; reusing `chunk_id` alone would let a rebuild replace
  or mix with the prior active projection.
- Reason: define one projection interface with two adapters. Put generation and
  document-version identity in both physical document IDs and payload filters,
  and compute observations from sorted chunk IDs/content hashes.
- Act: added generation-aware Qdrant and Elasticsearch upsert/scroll methods,
  content-hash payload fields, mapping upgrades, and a verifier that records both
  observations before readiness.
- Refine: keep legacy writes unchanged and defer pipeline `completed` semantics
  until manifest creation, verification, and restart behavior are integrated in
  one later slice. This limits the cutover seam while preserving compatibility.

## 2026-08-28 - P2.3 pipeline generation integration

- Perceive: expected chunk identity is unknown until streaming parse finishes,
  but creating the manifest after backend writes leaves unowned projection data.
- Reason: use a two-stage manifest lifecycle: persist immutable build identity
  before writes, then seal expected count/digest after parsing. Hide ordering,
  verification, and activation behind one generation-build interface.
- Act: added deterministic configuration-bound generation IDs, strict dual
  projection writes, idempotent sealing, verification, CAS activation, worker
  wiring, and a development projection adapter. Durable job completion now
  follows successful generation activation.
- Refine: generation retries must rewrite every deterministic chunk rather than
  trust legacy chunk-ID checkpoints. Partial embedding and Elasticsearch errors
  must fail closed; the old eventual-consistency path remains only for legacy
  messages. Read-side active-generation filtering remains a separate slice.

## 2026-08-28 - P2.3 active-generation query filtering

- Perceive: writing generation identity into derived indexes does not prevent a
  hybrid search or semantic cache from returning a stale, partial, or retired
  generation.
- Reason: keep lifecycle truth in PostgreSQL and place one batch visibility
  seam between backend candidates and fusion. Reuse it for cached candidates,
  because cache invalidation and expiry are optimizations rather than a
  correctness boundary.
- Act: propagated document-version/generation identity through both retrieval
  adapters and cached candidates, made Qdrant relevance and fusion identity
  generation-scoped, and wired a fail-closed PostgreSQL gate into Query API.
- Refine: compatibility must be document-scoped. A document with no manifests
  may expose historical legacy points, but the first manifest permanently
  moves that document under active-generation rules; incomplete identities and
  resolver failures cannot be treated as legacy.

## 2026-08-28 - P2.3 manifest reconciliation and idempotent repair

- Perceive: a manifest contains expected identity evidence, not the source
  chunks or embeddings, so it cannot safely reconstruct a missing projection.
  An active generation can also diverge after activation and must stop serving
  known-bad evidence without prematurely promoting another generation.
- Reason: keep scanning, fencing, diagnostics, and durable replay scheduling
  behind the PostgreSQL reconciliation seam. Reuse the original ingestion
  outbox task to rebuild the same deterministic generation, and treat repair
  attempts as consecutive failures rather than lifetime retries.
- Act: added fair leased claims, independent dual-backend observation,
  fail-closed divergence state, bounded atomic job/outbox reopening, pending
  replay deduplication, configurable worker scheduling, and fresh dual
  verification before a repaired active generation can complete and reappear.
- Refine: returning success merely because a replayed manifest is already
  `active` is unsafe; the projections may still be incomplete. Active replay
  completion now observes both backends and conditionally clears the durable
  error only when count and digest exactly match the sealed expectation.
- Verification: unit tests and disposable-schema PostgreSQL tests cover lease
  fencing, single replay scheduling, repair limits, healthy resets, concurrent
  activation, and active/legacy visibility. Retention and operational
  metrics/alerts remain separate P2.3 work.

## 2026-08-28 - P2.3 generation rollback and retention

- Perceive: rollback and garbage collection compete for the same retired
  generation. Verifying a rollback target without protecting it allows cleanup
  to delete one projection before promotion; using original activation time as
  the retention clock can delete a newly retired, long-running generation.
- Reason: use `retired_at` as the rollback-window boundary and one retention
  lease/fencing mechanism for both rollback protection and cleanup ownership.
  Keep cross-backend deletion ordering behind a collector interface while
  PostgreSQL owns final CAS transitions and durable partial progress.
- Act: added administrator-only explicit rollback with expected-active CAS,
  fresh dual-backend verification, exact-generation deletion adapters, leased
  cleanup claims, per-backend deletion timestamps, and cleanup-disabled
  configuration requiring an approved positive window before enablement.
- Refine: retiring a generation must invalidate outstanding reconciliation
  claims, and Elasticsearch `delete_by_query` can return HTTP 2xx with shard
  failures. State transitions now clear stale reconciliation fences, Qdrant
  waits for deletion acknowledgement, and Elasticsearch inspects partial
  failure fields before marking cleanup complete.
- Verification: unit and real PostgreSQL tests cover window enforcement,
  active/retired atomic exchange, stale CAS rejection, exact projection
  filters, partial cleanup retry, and manifest deletion only after both
  projections complete. Metrics/alerts and the final acceptance matrix remain.

## 2026-08-28 - P2.3 generation observability and acceptance

- Perceive: per-resource metric labels would turn enterprise tenant/document
  growth into unbounded Prometheus cardinality, while process-local counters
  alone would lose persistent failures after restart.
- Reason: publish current health from a bounded PostgreSQL snapshot and emit
  lifecycle pass outcomes through small optional observer seams. Keep every
  label value in a fixed state, condition, or outcome vocabulary.
- Act: added manifest count/age and durable diagnostic gauges, reconciliation,
  retention, and rollback counters, five tested sustained-condition alerts,
  and a documented crash/reindex/rollback/garbage-collection matrix.
- Refine: integration tests guarded only by an optional local DSN are not a CI
  acceptance gate. Go CI now provisions PostgreSQL 16, and the real rollback /
  retention lifecycle also exercises the operations snapshot query.
- Verification: focused TDD tests, promtool rule tests, a disposable PostgreSQL
  lifecycle test, and the full project quality suite cover the final P2.3
  engineering gate. Production rollout remains separately controlled.

## 2026-08-29 - P2.4-A version-bound release model

- Perceive: durable ingestion could replace the mutable document row while the
  prior publication state remained unchanged, and active generation uniqueness
  was version-scoped. No PostgreSQL identity named the one approved release.
- Reason: keep current admission and published authority as separate pointers
  behind one small release interface. Use a monotonic revision for stale
  candidate rejection and update current identity in the admission transaction.
- Act: added the release schema and PostgreSQL module, atomic admission wiring,
  idempotent current-version recording, tenant-scoped lookup, and publication
  compare-and-set. Migration resolves only exact object/job/generation matches.
- Refine: distinguish ambiguity from missing legacy evidence. Multiple possible
  versions or generations abort migration; no provable history becomes an
  explicit unresolved row so later governance fails closed without blocking the
  schema upgrade. Full Go tests, race checks, formatting, vet, and disposable
  PostgreSQL integration tests pass. No query policy, deployment, or retention
  setting changed in this slice.

## 2026-08-29 - P2.4-B exact-candidate approval

- Perceive: publication assessment trusted mutable document-level index counts,
  approval persisted only a document id, and document state, cache invalidation,
  and audit were separate best-effort side effects.
- Reason: keep Agent and HTTP callers on a small immutable-candidate interface;
  hide manifest health, release CAS, locking, idempotency, and atomic audit in a
  PostgreSQL publication module.
- Act: bound assessment and durable approval to version, generation, sealed
  count/digest, and release revision. Added approval-time row locking and exact
  revalidation, atomic document/release/audit commit, and an idempotency-key plus
  request-hash binding for safe response-loss replay.
- Refine: a key alone did not prove that a retry carried identical arguments,
  and an unlocked manifest left a validation/commit race. Candidate hashing and
  a shared manifest lock close both gaps. Focused Agent/module tests, disposable
  PostgreSQL transaction tests, and the full Go suite pass. Query cutover,
  deployment, and retention remain out of scope.

## 2026-08-29 - P2.4-C published-release cutover

- Perceive: generation-aware indexes still allowed a replacement generation to
  appear in query or content-search results before its approval, while default
  user uploads needed to retain their automatic-publication behavior.
- Reason: put one PostgreSQL release resolver at the retrieval seam so Qdrant,
  Elasticsearch, semantic cache, and document search share exact version and
  generation authority. Keep the previous release until approved cutover.
- Act: added fail-closed published-release visibility, idempotent automatic
  release advancement for `user-uploads`, replacement permission/space
  invariants, and continuity/search regression tests.
- Refine: automatic publication derives the active healthy generation instead
  of trusting caller input; malformed visibility responses are unavailable,
  never partially accepted. Deletion recovery and public acceptance remain
  separate P2.4 slices.

## 2026-08-29 - P2.4-D recoverable document deletion

- Perceive: synchronous deletion touched three dependencies before PostgreSQL,
  left a failed request query-visible, and could not reconstruct partial work.
- Reason: accept deletion as a PostgreSQL authority change first, then put
  leases, fencing, progress, retries, and finalization behind one deep deletion
  module. Keep this lifecycle independent from generation retention.
- Act: added atomic release revocation/job/audit acceptance, HTTP `202`, exact
  object-key snapshots, worker-side Qdrant/Elasticsearch/MinIO cleanup, durable
  partial progress, bounded metrics, and tested alerts.
- Refine: deletion must also fence late ingestion and approval writers. Queued
  work is cancelled, active leases delay cleanup, completion cannot auto-publish
  a pending deletion, and stale collector tokens cannot finalize. Exact object
  keys avoid ambiguous prefix deletion when document ids share prefixes.

## 2026-08-29 - P2.4-E public governance acceptance

- Perceive: unit and PostgreSQL tests proved individual invariants, but no one
  retained artifact showed that authenticated users observe continuity,
  independent approval, fail-closed deletion, dependency recovery, and audit
  correlation as one lifecycle.
- Reason: use public HTTP as the business evidence seam, bounded worker metrics
  for durable failure evidence, and Compose only for fault injection. Keep the
  real dependency run explicit while normal CI checks the harness contract.
- Act: added an isolated executable matrix covering Kafka/API/worker recovery,
  managed replacement cutover, Qdrant/Elasticsearch/MinIO deletion recovery,
  final `404`, and correlated audit. Reports recursively redact credentials and
  authorization data. The legacy smoke flow now polls after HTTP `202` deletion.
- Refine: an acceptance-only MinIO tmpfs avoids the host disk floor, but a
  container restart clears the bucket. Recovery must invoke the application's
  normal bucket initialization before judging retry behavior; otherwise the
  harness manufactures a permanent fault absent from the intended scenario.
- Verification: nine harness contract tests, syntax/Compose checks, and the
  isolated full-stack run passed all six scenarios with a secret-free JSON
  report. No deployment or generation-retention change was made.

## 2026-08-29 - P2.5-A policy-owned principal baseline

- Perceive: middleware accepted a signed token's tenant, role, and scopes as
  authorization facts and intentionally allowed unknown users for evaluation;
  the same behavior would permit claim-based authority and test identities in
  production.
- Reason: authenticate through one provider-neutral interface and return an
  internal `Principal`. Keep identity proof in adapters, authorization facts in
  internal stores, and knowledge-space membership in the catalog.
- Act: marked local/test/legacy methods, rebuilt known-user authority from
  PostgreSQL, applied a production fail-closed policy, enforced exact HS256,
  and made shared middleware depend only on the `Authenticator` interface.
- Refine: declaring an interface while keeping middleware on the JWT verifier
  would force a future OIDC adapter to duplicate context and scope behavior.
  Moving middleware to the seam makes adapter replacement real and testable.
- Verification: focused identity and Query API tests cover claim injection,
  algorithm confusion, unknown/inactive/revoked users, production test/legacy
  rejection, and non-production evaluation compatibility. Enterprise provider
  integration remains correctly blocked on ADR 0009 decisions.

## 2026-08-30 - P2.5-B external identity binding foundation

- Perceive: the principal seam reserved federated authentication, but there was
  no durable provider identifier mapping and the chosen IdP remained unknown.
- Reason: isolate stable identity mapping from provider credential validation.
  Store only issuer, exact subject, and internal user identity; resolve mutable
  authorization from the current user row on every authentication.
- Act: added tenant-safe bindings, a fail-closed directory, and admin management
  whose create/delete audit records share the mutation transaction. Subject is
  available to tenant admins for management but deliberately omitted from audit.
- Refine: a handler-only tenant check is insufficient for alternate callers,
  and skipped integration tests are not a CI gate. The management module repeats
  admin/tenant enforcement, while CI now supplies the real PostgreSQL test DSN.
  Go format/vet/full/race, fresh PostgreSQL migrations, 128 script tests,
  Parser 32, Reranker 1, Webhook 3, Web audit/lint/build, Compose, Prometheus,
  Alertmanager, and Grafana checks passed. The deterministic full-stack eval
  could not start because the host filesystem had no free space; no provider
  adapter, federation enablement, or deployment occurred.

## 2026-08-30 - P2.5-C OIDC authorization-code foundation

- Perceive: external identity bindings existed, but no provider credential was
  validated and the browser had only a password flow. A direct IdP-token
  middleware would have mixed provider claims with internal authorization and
  exposed rotation, callback, and multi-replica complexity to every caller.
- Reason: keep one deep code-exchange module above the existing directory seam.
  Treat issuer/subject as identity only; re-resolve tenant, role, active state,
  and capabilities internally. Test the module and HTTP seams, not private JWKS
  helpers.
- Act: used red/green slices for a valid RS256 exchange, bounded key rotation,
  PKCE/state/nonce single-use flow, production local-login shutdown, federated
  platform sessions, cross-replica Redis transactions, and the Web BFF cookie
  flow. Added strict configuration and default-off Compose wiring.
- Refine: an in-memory transaction store is correct only for development; Redis
  `GETDEL` gives staging/production atomic consumption across replicas. IdP
  tokens never enter Redis or browser JavaScript, and return paths cannot become
  open redirects. Keycloak remains an acceptance target rather than an implied
  production-provider decision; SCIM, groups, service identity, break-glass,
  and deployment stay gated.
- Verification: full Go and race suites, PostgreSQL and cross-instance Redis
  integration, 128 script tests, Parser 32, Reranker 1, Webhook 3, Web
  audit/lint/build, Compose, Prometheus, Alertmanager, Grafana, Trivy, and the
  47-case deterministic full-stack eval passed. The protocol tests use a real
  TLS OIDC test provider. An explicit full Authorization Code + PKCE acceptance
  flow also passed against Keycloak 26.3.3 over HTTPS. Review then caught and
  closed redirect-based client-secret exposure, same-`kid` JWKS rotation,
  browser/backend TTL drift, and insecure federated-cookie configuration.

## 2026-08-30 - P2.5-D identity lifecycle design

- Perceive: OIDC now proves a pre-bound identity, but the current user model has
  globally unique local usernames, mandatory password hashes, and no durable
  provisioning ownership or deprovisioning tombstone.
- Reason: isolate provider protocol from internal lifecycle authority. A deep
  provisioning module should atomically own the user, external binding,
  revocation, idempotency, tombstone, and audit transition; a SCIM adapter only
  translates requests into that seam.
- Act: documented connector-to-tenant binding, least-privilege defaults, exact
  subject mapping, immediate revocation, retained tombstones, bounded SCIM
  behavior, operational evidence, and two proposed test seams.
- Refine: neither email/userName nor organization/group claims safely identify
  tenant, authorization, or OIDC subject across providers. Those mappings and a
  possible JIT fallback remain explicit enterprise decisions before code is
  implemented.

## 2026-08-30 - P2.5-D identity lifecycle implementation

- Perceive: the approved design required one atomic ownership boundary rather
  than SCIM handlers coordinating users, bindings, revocation, audit, and replay
  independently. Production provider selection and enablement remained outside
  the authorized slice.
- Reason: test through `Provisioner.Apply` with real PostgreSQL and through the
  SCIM HTTP contract. Connector policy, not provider profile data, fixes tenant,
  issuer, subject attribute, and least-privilege role.
- Act: added migration `0020`, the lifecycle provisioner, a bounded default-off
  Users adapter, federated-only login/admin safeguards, secret overlap,
  configuration, metrics, and failure/staleness alerts. Create, mutation,
  revocation, tombstone, audit, and idempotency commit atomically.
- Refine: PUT needs explicit present-versus-empty profile fields; plain strings
  silently prevented IdP-requested clearing. Pointer fields now preserve
  replacement semantics. Resource paths, metadata, identity, and profile fields
  are bounded before persistence; repeated deactivation revokes sessions only
  on the active-to-inactive transition.
- Verification: Go formatting, vet, ordinary and race suites passed with real
  PostgreSQL integration. The 128 script tests, Parser 32, Reranker 1, Webhook
  3, Web audit/lint/build, Compose overlays, Prometheus/Alertmanager/Grafana,
  both Trivy CRITICAL gates, and the 47-case deterministic full-stack eval all
  passed; the eval reported 100% pass, answer, retrieval, and negative rates.
- Final review closed four spec gaps before handoff: raw bearer values are
  discarded after hashing, SCIM mutation latency is measured without reads
  masking stale synchronization, audit carries a bounded request correlation
  ID, and explicit tenant-isolation/HTTP-concurrency tests supplement the
  PostgreSQL replay proof. Origin constants also centralize lifecycle ownership.
- Re-review tightened those guarantees: the constructor clears the caller's
  credential slice, readiness and successful synchronization use distinct
  timestamps so restarts cannot fake success, and the server generates a
  correlation ID when the provider omits or overflows `X-Request-ID`.

## 2026-08-30 - P2.5-E production identity acceptance design

- Perceive: provider-neutral OIDC and SCIM modules are complete, but production
  identity still depends on business-owned provider, tenancy, subject, privacy,
  SLA, retention, rotation, MFA/session, and emergency-access decisions.
- Reason: preserve the three existing deep module interfaces. Compare true
  external IdPs at their adapters and accept them only through the same composed
  SCIM-to-internal-identity-to-OIDC seam used by staging evidence.
- Act: documented a weighted Entra ID/Okta/Keycloak evidence matrix, mandatory
  rejection gates, named decision owners, a public-interface lifecycle/outage/
  rotation sequence, signed evidence bundle, expiry triggers, and later P2.5-F
  through J slices.
- Refine: Keycloak's passed OIDC protocol exercise does not prove a production
  SCIM profile or make it the default choice. Product documentation alone is
  not acceptance evidence, and proposed numeric policies remain unapproved
  until their enterprise owners sign them.
- Review: successful SCIM mutation age is not authoritative reconciliation
  evidence. P2.5-J now owns drift comparison, retry, missed-run alerting, and a
  distinct completion signal. The public template is an index for private,
  hashed, signed evidence, and concurrency acceptance fixes one explicit
  idempotency key.

## 2026-08-30 - P2.5-F 企业会话安全设计

- 感知：当前浏览器会话是固定 24 小时的 HS256 JWT 和 Cookie；当前用户查询支持
  用户级停用/版本撤销，但退出只清除 Cookie，且不存在独立持久会话。
- 推理：认证协议证据、会话策略和业务操作风险因不同原因变化。提供方声明转换留在
  适配器中，把到期、新鲜度、轮换和撤销放到一个深层、与提供方无关的会话接口后。
- 行动：记录由注册表支持的目标模型、独立会话时钟、认证保证批准要求、风险驱动
  重新认证、本地优先退出、精确身份批次迁移及明确实现切片。
- 改进：现有 24 小时寿命不代表企业批准。所有数值和选定提供方的
  `acr`/`amr`/`auth_time` 语义仍为 `Pending`；缺少证据时失败关闭，而紧急访问
  保持隔离在 P2.5-G。
- 评审：IdP 会话寿命必须独立审批和验证；生产切换前要演练不会导致全员锁死的
  联邦配置回滚目标；首次建会话和重新认证都要覆盖认证保证的强弱正反例。

## 2026-08-30 - P2.5-G 企业紧急访问设计

- 感知：生产联邦策略当前没有紧急路径；bootstrap admin 只是空库初始化的普通
  本地管理员，且登录审计 best-effort，无法证明多人批准、短期租约或最小权限。
- 推理：常备恢复能力不等于常驻平台权限。将保管/证明适配器、事故租约、逐请求
  动作授权、原子审计和通知 outbox 集中在独立 `emergencyaccess` 深层模块中。
- 行动：定义主 IdP 独立故障域、操作人 challenge 持有证明、多方审批、短期租约、
  非 `admin` 动作白名单、自动撤销、材料轮换、独立复盘和 canary 演练契约。
- 改进：审计或租约存储故障必须阻止紧急权限，而通知发送从已原子持久化的 outbox
  重试；PostgreSQL/控制面故障属于基础设施灾难恢复，不能用应用后门掩盖。
- 接口复核：只返回紧急 principal 会把动作结果审计泄漏到处理器。改由 `Execute`
  在副作用前持久化幂等意图，再调用受限适配器并收敛外部不确定结果。
- 审查：规范与规格双轴均通过；独立故障域、多人控制、认证器持有证明、短期租约、
  最小动作、失败关闭、通知、复盘和 canary 演练没有遗留可行动缺口。

## 2026-08-30 - P2.5-H 企业组到知识空间授权设计

- 感知：当前知识目录只有直接成员，manager 无成员管理路由；OIDC 不解析组，SCIM
  Users 拒绝 groups/roles，因此不存在可直接启用的权威组授权输入。
- 推理：稳定外部组、租户拥有的映射和请求时有效权限因不同原因变化。分别放入
  `groupdirectory`、`groupmapping` 和现有 `knowledgecatalog` 接缝，raw token
  claim 只用于诊断。
- 行动：定义完整版本快照、稳定组 ID/tombstone、同租户空间角色映射、直接/组来源
  合并、最高允许角色、下一请求撤权、修订绑定缓存、对账及 shadow/canary 迁移。
- 改进：组 claim overage 或省略不是空成员集合；超过批准陈旧窗口时只让组派生
  授权失败关闭，不能误删仍有效的人工直接授权。
- 审查修正：异步 outbox 不能单独保证下一请求撤权，缓存 allow 必须先通过同步修订
  栅栏；跨页目录同步需要 snapshot token/watermark 或首尾版本变化即整轮重试；回滚
  直接成员必须逐条 CAS 且冲突转人工对账；验收补充重新认证、审批分离、重放和即时
  删除/降级测试。

## 2026-08-30 - P2.5-I 企业服务与工作负载身份设计

- 感知：代码只预留 service 认证方法，实际内部链路依赖共享 token 或资源 key；
  Principal 的 subject 又被普遍当作 user ID，直接接入机器身份会造成身份混淆。
- 推理：业务 workload、平台 workload、委托执行和资源凭据具有不同授权语义。通过
  `workloadidentity` 深层模块隐藏协议差异，并让资源模块继续拥有最终授权。
- 行动：定义 human/workload kind、精确 issuer/subject/audience、内部 grant 交集、
  同步撤权栅栏、短期 sender-constrained 凭据、轮换及基础设施身份迁移。
- 改进：异步任务必须同时记录人类发起者、执行 workload 和有界委托；浏览器 token
  不进入队列，worker 自身身份也不能成为跨租户的永久代理权限。
- 审查修正：委托签发和每次副作用都必须受发起者当前权限与执行器 grant 的交集约束；
  `private_key_jwt` 只认证客户端，必须另用 mTLS/DPoP 绑定 access token，或按 bearer
  例外执行重放防护，不能把它误当成 sender-constrained 证明。

## 2026-08-31 - P2.5-J 提供方适配、权威对账与 Staging 验收设计

- 感知：A～D 已有实现，F～I 仍是设计；Keycloak 运行只证明 OIDC 协议兼容，SCIM
  mutation success 也不能证明一次 IdP-authoritative 全量对账完成。
- 推理：provider 差异分别留在协议 adapter，对账复杂性收进单一 `Run` 接口，验收
  driver 只通过公开 seam 观察，避免 provider payload、修复 SQL 和证据逻辑扩散。
- 行动：定义完整快照、双向漂移、dry-run/apply、lease/fencing、幂等修复、独立完成/
  失败指标、隔离 staging 组合矩阵、2.0 manifest 和后续 production PR 门禁。
- 改进：设计审批、实现完成、staging 验收和生产启用是四个不同状态；blocked/skipped
  mandatory 项不能包装成 pass，缺少真实企业输入时也不能替用户选择 IdP。
- 审查修正：approved Schema 必须强制非空 provider/platform/policy/对账/证据和成熟度；
  每项修复事务内校验 fence/profile；无 provider revision 时用双遍资源 digest；签名对
  排除 envelope 的 canonical payload 生成，并为各责任人保留独立 detached signature。
- 复审修正：风险接受可以为空，但存在时必须有 risk ID、责任角色、到期时间和证据；
  approved manifest 精确列出八类失效触发器，并把签名 canonicalization 固定为 RFC 8785。

## 2026-08-31 - P2.5 企业决策登记与实现准入

- 感知：E～I 共有 51 项跨身份、租户、安全、产品、隐私、法务、运维、SRE、SOC、
  合规和业务连续性的 `Pending` 输入；代码仓库没有权威企业责任人或真实 provider。
- 推理：建议、文档审批和企业策略审批是不同事件。稳定决策 ID 与不可变私有证据引用
  能让 PR 逐行评审，同时避免在仓库保存姓名、tenant、凭据或恢复材料。
- 行动：建立 E-01～I-10 登记表、审批状态机、证据/失效规则和逐阶段准入矩阵；同步
  README、架构、技术设计和 backlog，保持运行代码、真实接入与 production 不变。
- 改进：只有全部所需责任人签署同一未过期 revision 才能解锁既定实现顺序；推荐基线、
  部分签署或合并本文档都不能被自动解释为 `Approved`。

## 2026-08-31 - P2.5 个人演示身份策略

- 感知：用户目标是个人演示；仓库已有 Keycloak 26.3.3 OIDC 接缝测试，但主 Compose
  未内置 Keycloak，F～I 运行模块也尚未实现，不能把设计值描述为已运行能力。
- 推理：个人所有者可以批准本地实现参数，却无法提供真实企业的职责分离、法务、SOC
  或业务连续性签署。独立 `DemoApproved` namespace 可以继续实现而不污染企业证据。
- 行动：为 E～I 的 51 项设定 Keycloak/虚构数据的具体 demo 值，建立 PD0～PD3 gate，
  保持企业 D0～D7 blocked，并定义不接管登录流的 P2.5-F1 会话核心 TDD 切片。
- 改进：demo/simulation 必须在配置、UI、报告和证据中显式标记；staging/production
  拒绝 demo profile，真实 secret 仍只进入 ignored 文件或 secret mount。

## 2026-08-31 - P2.5-F1 会话核心

- 感知：现有登录签发 24 小时自包含 JWT，没有 session ID、持久状态、空闲到期、
  认证保证新鲜度、逐会话撤销或轮换；本切片明确不能提前接管登录。
- 推理：会话模块只拥有认证状态；role/capabilities 仍需从用户与知识目录实时解析。
  credential 明文只返回调用方，存储保存 SHA-256；generation CAS 关闭撤销竞争窗口。
- 行动：以 `Establish`/`Authenticate`/`Revoke` public seam 完成 red→green 循环，新增
  migration、内存/PostgreSQL adapters、到期/重新认证/撤销/轮换和 dev-only 配置门禁；
  撤销 correlation 与撤销状态在同一存储操作中提交。
- 改进：区分了有效 red、测试编译错误和意外 green；数据库错误返回 unavailable，而
  并发安全状态变化返回 deny。审查还识别并修复了重新认证延长绝对寿命、主体撤销与
  并发建立竞态、建立审计关联可空三项问题；主体锁和 `revoked_before` 水位在真实
  PostgreSQL 中证明旧认证无法逃逸。模块仍 default-off 且未在 main 构造，不改变现有行为。

## 2026-08-31 - P2.5-F2 会话凭据验证

- 感知：F1 已有 session 状态机，但 HTTP 只识别 JWT；`session` 又依赖 `auth.Principal`，
  若让 `auth` 反向依赖 `session` 会形成包循环。
- 推理：保持 `auth.Authenticator` 为公共 seam，在 API 装配层组合两个 adapter。显式版本
  前缀比“JWT 解析失败后再试 session”更安全，因为 credential 类型不会随错误路径降级。
- 行动：以 HTTP seam 的 red→green 测试接入 `ps1_` 路由、失败关闭、JWT 兼容和实时
  userstore 权限解析；仅 dev + `personal-demo-v1` + 显式开关时构造 PostgreSQL adapter。
- 改进：session 只证明认证状态和内部定位，不保存或恢复权限快照。F2 对全部保护路由
  使用 standard `platform.request`，明确不虚构高风险重新认证能力；签发、Cookie、OIDC、
  logout 和 reauthentication 事务仍留给后续切片。

## 2026-08-31 - P2.5-F3a 风险判断与重新认证响应

- 感知：F2 把所有路由视为 standard，无法让 session 的新鲜度状态机进入真实 HTTP
  请求；同时完整 OIDC 重新认证还缺可信证据转换、事务绑定和 credential 签发。
- 推理：先把 method/path 到策略 action 的分类和结构化响应稳定下来，使 F3b 只需实现
  “如何获得新证据”，而不再重写业务路由或错误语义。未登记路由保留 standard，避免
  把普通读取误升级为高风险；敏感动作必须显式列入策略 map。
- 行动：以 HTTP seam 的 red→green 测试覆盖风险矩阵、新鲜/陈旧证据、JWT 兼容、普通
  路由和错误区分；达到 10 分钟边界返回 `401 reauthentication_required`。
- 改进：reauthenticate 不能早于实时撤权检查。session 决策因此只返回内部定位，adapter
  先校验 active/tenant；停用用户、无效凭据和存储故障仍统一 unauthorized，不能收到
  可继续流程的挑战。F3a 不声称已实现 MFA 或重新认证事务。

## 2026-08-31 - P2.5-F3b 可信认证证据与单次重新认证事务核心

- 感知：F3a 已能识别高风险请求，却没有可信方式把 Keycloak 声明转换为内部认证证据，
  也没有把新的认证尝试绑定到当前 session credential、内部主体和目标动作。
- 推理：提供方语义应止于 OIDC adapter；Flow 只编排 provider-neutral evidence 和一次性
  transaction。普通登录与重新认证使用独立完成入口，避免弱登录误入强认证路径或反向绕过。
- 行动：以 public seam 的 red→green 测试实现严格 `acr`/`amr`/`auth_time` 转换、新的
  state/nonce/PKCE、credential 摘要及 tenant/subject/action 绑定、恒定时间比较和单次消费；
  Redis 测试证明不同实例可完成且重放失败。
- 改进：完成入口必须双向拒绝事务类型混用，并在 token exchange 后重新核对目录解析出的
  内部主体。凭据变化、主体漂移、含糊/陈旧证据、事务存储或 IdP 故障均不产生部分成功。
  本切片不装配 HTTP、不签发/轮换 `ps1_`、不改 Cookie，保持后续 F3c 的责任清晰。
- 审查修正：事务过期必须与 Memory/Redis adapter 使用同一真实时钟域，不能误用仅供 token
  测试的注入时钟；`auth_time` 的批准证据不接受任何未来值。公共 OIDC transaction builder
  和集中 demo evidence 常量同时消除了普通/重新认证流程及两层证据校验的策略漂移。

## 2026-08-31 - P2.5-F3c HTTP 重新认证与原子会话轮换

- 感知：F3a challenge、F3b transaction 和 session 原子 rotation 都已存在，但没有一条
  HTTP/Cookie 路径把三者连接起来；另建浏览器 callback 会与 token exchange 的既有
  redirect URI 不一致。
- 推理：服务端风险分类应随结构化 challenge 返回，Web 不复制策略。普通登录与重新认证
  复用唯一 OIDC callback URL，用路径受限的独立 state Cookie 分流，并保持后端 handler 独立。
- 行动：以 Query API public HTTP seam 的 red→green 测试实现 eligible start、可信 callback、
  实时用户校验、CAS rotation、重放/并发拒绝及脱敏审计；Web BFF 增加显式同源 POST、
  Secure/HttpOnly state 与主 Cookie 编排，客户端只跳转而不自动重放写操作。
- 改进：callback 在 middleware 的 standard action 下验证当前 credential，避免陈旧会话无法
  到达恢复入口；真正的 high-risk action 仍在 handler/transaction 内核对。真实 Next server
  与 mock backend 验证了跨站 403、成功 Cookie rotation，以及失败只清 state 不改旧 session。
