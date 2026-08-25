# Codex Learnings

This file is an append-only record of completed PRAR cycles.

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
