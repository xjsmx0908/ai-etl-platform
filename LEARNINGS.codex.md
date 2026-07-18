# LEARNINGS.codex.md

## 2026-07-18T11:33:43+08:00 - Module 4 Release Verification

Perceive: Module 4 combined query tracing, LLM dependency alerting, an enterprise notification adapter, deterministic evaluation, an optional Judge, and exact-candidate protection. Before publishing the accumulated work, the full integration needed a current verification run rather than relying on earlier focused checks.

Reason: a release-level result must distinguish deterministic engineering regression evidence from production-quality claims, protect local secrets, and prove that isolated evaluation cleanup does not interrupt the active development stack.

Act: reran Go format/vet/tests, parser and reranker tests, alert relay and secret-permission tests, Prometheus/Alertmanager/Grafana validation, evaluation-script tests, Trivy critical vulnerability and misconfiguration gates, and both isolated deterministic suites. The default set passed 47/47; the clearly labeled synthetic learning set passed 100/100. Both evaluation projects were removed afterward.

Refine: confirmed real local webhook files, private historical input, `.env`, and generated reports remain Git-ignored; the default Query API and alert relay are healthy. The synthetic result validates platform regression contracts only. Enterprise acceptance and external Judge quality claims still require approved, deidentified historical data in the private intake path.

## 2026-07-13T23:32:08+08:00 - Module 4 Reranker Failure Exact-Protection Regression

Perceive: the default 47-case CI evaluation failed only on the metadata-only exact-reference case. Disabling semantic cache did not change the failure, while a retained isolated stack showed the target record and `customer_ref` metadata correctly persisted in both Qdrant and Elasticsearch.

Reason: Query logs showed the engine found one exact candidate, but the optional reranker endpoint was configured without a running reranker service. The failed rerank path recorded a partial error and kept fused ranking, bypassing the exact-candidate pinning that protects the same query when reranking is disabled.

Act: added a focused Engine test for a semantic metadata exact candidate with a failing reranker. The test failed before the change. The Engine now pins exact candidates from fused ranking when reranking is skipped or fails, emits the reason `reranker_failed_exact_candidate_pinned`, and retains the reranker partial error for operations.

Refine: the focused regression test, full Go format/vet/test suite, parser and reranker Python tests, alert/config tests, Trivy vulnerability and configuration gates, and the default 47-case isolated evaluation all passed. The final eval restored `case-047` to strict rank 1 with 100% retrieval and answer assertions; all isolated stacks were removed afterward.

## 2026-07-13T21:47:48+08:00 - Module 4 Judge No-Answer Contract

Perceive: the first isolated Mock-Judge smoke evaluation passed deterministic retrieval and answer assertions for eight cross-module cases, but Judge scoring passed only the four positive cases. All four valid permission-refusal cases were scored as failures.

Reason: the Mock Judge recognized only `not found` and generic `cannot answer` text, while the Query mock can produce the separately approved fallback `not directly located in the reference documents`. A protocol mock must honor the same no-answer contract as the evaluation runner or it creates false failures.

Act: added a focused HTTP regression test for the documented fallback, then extended the local Mock Judge's refusal recognition to include it. The test failed before the change and passed afterward.

Refine: reran the original eight-case isolated Judge smoke set: all retrieval and answer assertions passed, the Mock Judge attempted and passed all eight cases with no errors and 5.0/5 average faithfulness. This only validates local protocol and threshold wiring; real historical data and an approved external Judge are still required to assess model quality.

## 2026-07-13T12:50:30+08:00 - Module 4 Full Engineering Learning Evaluation

Perceive: the 100-case learning set and its isolated Compose runner had passed structural and unit-level validation, but the full end-to-end evaluation had not yet exercised ingestion, retrieval, answer assertions, negative permissions, and cleanup together.

Reason: a large synthetic set can prove the regression harness and platform contracts only when it runs through the same isolated stack as CI-style evaluation. It must remain explicitly separate from a real historical enterprise acceptance set.

Act: validated all 100 cases and ran `scripts/run-evals.py` against an isolated `ai-etl-eval-*` Compose project with dynamic ports. The run passed all 88 positive and 12 negative cases: retrieval, answer, final assertion, and Recall@1/3/5 were each 100%.

Refine: verified the isolated project was removed and the existing Query API, Alertmanager, and alert relay remained running. This is a deterministic synthetic regression result, not evidence of production business quality; the private 100-case historical intake remains required before enterprise acceptance or external Judge evaluation.

## 2026-07-13T13:50:00+08:00 - Module 4 Isolated Evaluation Runtime

Perceive: the evaluation runner started and destroyed the default Compose project. Running the new 100-case learning set would therefore interrupt the active development stack, contradicting the operational safety expected from an evaluation tool.

Reason: project name, host-port allocation, API discovery, and cleanup belong behind the runner's interface. Callers should supply a dataset, not learn a long list of Docker port overrides or remember which stack a destructive cleanup targets.

Act: the runner now creates a unique Compose project by default, sets all exposed host ports to Docker-assigned values, discovers the Query API mapping, and passes the same environment to cleanup. Default local port mappings remain unchanged outside the runner.

Refine: added unit tests for isolated project naming, port environment construction, and Compose-port parsing; validated both default and all-random Compose configurations. Full 100-case execution remains deferred until an isolated stack is intentionally started.

## 2026-07-13T13:35:00+08:00 - Module 4 Engineering Learning Evaluation Set

Perceive: no approved enterprise history was available, so the production historical gate could not be honestly satisfied. The existing 47-case fixture was too narrow to support structured learning across all four modules.

Reason: the useful fallback is a clearly labeled synthetic learning set, not a fabricated claim of enterprise history. It should be large enough to exercise the same validator and runner contract while retaining explicit limits on what results mean.

Act: added a 100-case synthetic set spanning ETL, retrieval, Agent, and observability. Each module has 22 answerable cases and 3 permission-refusal cases; every case has deidentified evidence, a stable anchor, module metadata, and a reference answer.

Refine: added a strict dataset gate test and a `run-evals.py` load test. The full Docker eval was not run because it tears down and recreates the shared Compose stack; use it in an isolated evaluation environment.

## 2026-07-13T13:20:00+08:00 - Module 4 Historical Evaluation Dataset Gate

Perceive: the repository documented a requirement for 100 deidentified historical questions with reference answers, but the requirement was not machine-checkable. The committed 47-case engineering set is useful for deterministic regression, yet it must not be mislabeled as historical enterprise evidence.

Reason: inventing 53 additional cases would create a misleading metric. A real production gate needs a private dataset path, explicit structural requirements, high-confidence secret/PII checks, and a clear boundary between public CI fixtures and approved historical evaluation data.

Act: added an ignored private dataset directory, an empty template, an intake guide, and a standard-library validator. The validator requires 100 cases, unique IDs, required evidence and standard-answer fields, and rejects high-confidence sensitive patterns without echoing matched business text.

Refine: added tests for valid 100-case input, duplicate/missing reference answers, and privacy-safe sensitive-pattern reporting. Judge execution against the private dataset remains a controlled human-data step, not a public CI job.

## 2026-07-13T13:05:00+08:00 - Module 4 Alertmanager Secret Permission Recovery

Perceive: after the webhook adapter was fixed, Alertmanager still could not read its `authorization.credentials_file`; the official image runs as `nobody`, while local Compose secrets retained host-only permissions. The failed notification stayed in Alertmanager's retry loop, so no message reached WeCom.

Reason: both sides of the authenticated callback must preserve least privilege. Running Alertmanager permanently as root or changing the host secret to world-readable would remove the symptom but weaken the deployment.

Act: added a small Alertmanager image wrapper that copies the token and rewrites its copied config to point to a private `nobody`-owned runtime directory, then launches the original binary through BusyBox `setuidgid`. Added a root-only secret container test that also asserts PID 1 runs as UID 65534.

Refine: rebuilt both services and sent a two-minute controlled critical alert through the Alertmanager v2 API. The adapter recorded a successful `POST /alerts` with status 202, which is emitted only after WeCom accepted the formatted message. The temporary test alert resolves automatically.

## 2026-07-13T12:45:00+08:00 - Module 4 Local Secret Permission Recovery

Perceive: the alert webhook service restarted immediately after real local secret paths were configured. Its `app` user received `PermissionError` reading `/run/secrets/alert_webhook_token`, while root could read the same mounted file. Docker Compose local secrets are bind mounts, so host `0600` ownership does not automatically match the image user.

Reason: relaxing a credential file to world-readable would mask the bug but weaken security. The service should preserve a non-root runtime identity while handling the Docker local-secret ownership mismatch only during startup.

Act: added a root entrypoint that copies configured secret files to a private, `app`-owned temporary directory, updates only the `_FILE` paths, and executes the Python process through `su-exec app:app`. Added a container-level regression test using a root-only mounted token and added it to CI.

Refine: verified root can read and `app` cannot read the original Compose-mounted secret, then ran the corrected container test three times plus unit tests. The source mount remains `0600`; only the in-container ephemeral copy is readable by `app`.

## 2026-07-13T12:30:00+08:00 - Module 4 LLM Alerting and Dashboard

Perceive: Query API exposed aggregate HTTP metrics and circuit state, but there was no LLM-stage request, outcome, latency, or consecutive-failure signal. Prometheus had no Alertmanager route, enterprise notification delivery, or provisioned dashboard. The API returns JSON rather than SSE, so Token/s could not be measured honestly.

Reason: production alerting should measure the dependency boundary directly, keep labels low-cardinality, combine an exact per-process failure streak with rolling rate and latency signals, and route through Alertmanager. Third-party webhook secrets should stay outside Prometheus configuration and source control.

Act: added LLM outcome, duration, and consecutive-failure metrics; initialized the configured model's zero-value gauge at Query API startup; connected the existing circuit breaker to real LLM calls; added tested Prometheus alerts; configured Alertmanager; built an authenticated WeCom/DingTalk adapter with secret files and DingTalk signing; and provisioned a Grafana overview dashboard.

Refine: added Go, Python, Prometheus rule, Alertmanager config, Grafana JSON, and Compose validation to local verification and required CI. Query tests also prove that six failed LLM calls open the breaker and the next rejected request is classified as `circuit_open`. Token-speed alerting remains intentionally deferred until a real streaming Query contract can expose time-to-first-token and output-token rate.

## 2026-07-13T11:13:43+08:00 - Module 4 Optional LLM-as-a-Judge

Perceive: the deterministic eval already measured retrieval and answer assertions, but it could not score semantic answer quality or faithfulness. Its generated report also omitted full answer evidence, making a detached post-processing Judge unable to evaluate grounding safely.

Reason: enterprise evaluation should combine deterministic metrics with a non-deterministic Judge rather than replacing one with the other. Judge inputs should be captured while answers and retrieved contexts are in memory, outputs should follow a strict schema, and external Judge failures must remain visible in reports.

Act: added an optional OpenAI-compatible Judge client, strict structured scores, bounded retries, per-case and aggregate reporting, threshold gates, deterministic mock support, and a manual GitHub Actions workflow that uses protected credentials.

Refine: added standard-library tests for schema requests, transient retries, invalid scores, real mock HTTP compatibility, and Markdown/JSON report generation. The existing required CI eval remains deterministic; production-quality Judge data still requires at least 100 desensitized historical questions with reference answers.

## 2026-07-13T10:56:42+08:00 - Module 4 Query Trace Topology

Perceive: OpenTelemetry and Jaeger were configured, but Query observability consisted mainly of one handler span and embedding spans. Retrieval routing, cache, backend scatter-gather, fusion, reranking, prompt construction, and final LLM generation could not be distinguished in a trace.

Reason: a useful production trace must preserve one W3C TraceID at HTTP boundaries and split the Query pipeline into stage-level spans. The client-facing TraceID is also needed so support can connect a failed API request to Jaeger without exposing high-cardinality identifiers in Prometheus.

Act: added HTTP trace extraction and `X-Trace-ID` responses, browser CORS exposure, outbound `traceparent` injection, Query and LLM spans, and retrieval spans for embedding, cache, routing, concurrent backends, fusion, reranking, and cache storage.

Refine: added tests proving incoming trace continuation, outgoing propagation, and the expected retrieval span topology; verified `gofmt`, `go vet ./...`, `go test ./...`, Docker Compose configuration, and `git diff --check`.

## 2026-07-05T16:52:28+08:00 - Module 3 Agent Observability and Alerts

Perceive: Module 3 had durable Agent state, approval audit, distributed locks, and lifecycle governance, but operations could only inspect individual runs through the API. There were no Agent-specific Prometheus signals for dashboards or alerts.

Reason: enterprise Agent orchestration needs low-cardinality event metrics, not per-run labels. Useful signals are run creation, terminal outcomes, normalized error types, tool-step states, approval decisions, and run/step duration histograms.

Act: added Agent Prometheus metrics, wired Agent API lifecycle changes through an observer interface, recorded approval decisions, added alert rules for failures, lifecycle timeouts, and high p95 run duration, and documented the new observability surface.

Refine: verified `gofmt`, `go vet ./...`, `go test ./...`, `docker compose config --quiet`, `promtool check rules`, and `git diff --check`.

## 2026-07-04T18:26:44+08:00 - Module 3 Agent Run Lifecycle Governance

Perceive: Agent runs had durable state, approvals, Redis persistence, and distributed locks, but an operator still could not cancel a non-terminal run and stale pending approvals had no timeout closure.

Reason: enterprise Agent orchestration needs lifecycle boundaries in addition to planning. Runs should fail deterministically when their wall-clock budget is exhausted, pending approvals should not stay pending forever, terminal runs should stay immutable, and approval audit must be synchronized when a run is closed by timeout or cancellation.

Act: added cancellation metadata, `POST /v1/agent/runs/{id}/cancel`, run timeout enforcement, approval timeout enforcement, terminal resume/cancel conflicts, approval-audit rejection on lifecycle timeout or cancel, and config through env and Docker Compose.

Refine: verified `gofmt`, `go vet ./...`, `go test ./...`, `docker compose config --quiet`, and `git diff --check`.

## 2026-07-04T17:52:52+08:00 - Module 3 Redis Distributed Lock

Perceive: the Agent run store and approval store were Redis-backed outside dev, but the Orchestrator still used an in-process memory lock. That protects a single process only and leaves multi-replica `query-api` unsafe.

Reason: a distributed state machine needs both lease ownership and fencing. The lock prevents concurrent execution, while the store fencing token rejects stale writes after lease expiry or takeover. Same-owner reacquire should also be rejected because one service instance can receive concurrent requests for the same run.

Act: added `RedisLockManager`, persisted owner and monotonic token in Redis, tightened MemoryLockManager acquire semantics, wired Redis locks into non-dev Agent API startup, and closed Redis lock resources with the service.

Refine: verified `go vet ./...`, `go test ./...`, real Redis lock contract with `AGENT_REDIS_LOCK_TEST_ADDR=127.0.0.1:6379`, `docker compose config --quiet`, and `git diff --check`.

## 2026-07-04T17:17:26+08:00 - Module 3 External Approval and Audit

Perceive: the Agent API had a `/approve` endpoint, but approval lived only in the current HTTP request by appending a tool name to `ApprovedTools`. That is not durable enough for enterprise side-effecting tools.

Reason: approval should be its own tenant-scoped audit record with pending/approved/rejected states. The state machine should continue from a durable approved record, and rejection should fail the pending tool step without executing the side effect.

Act: added memory and Redis approval stores, idempotent per-step approval ids, approval listing, approve/reject endpoints, `agent:approve` enforcement, rejection state transitions, and resume-from-approved-audit behavior.

Refine: verified `go vet ./...`, `go test ./...`, `docker compose config --quiet`, and `git diff --check`. Tests cover approval store tenant isolation, API approve/reject audit records, non-approver 403, cross-tenant not-found, and resume from persisted approval.

## 2026-07-04T15:56:45+08:00 - Module 3 Task Status Tool

Perceive: adding a second Agent tool should not be a fake wrapper. The platform did not have a shared task-status table; upload responses only returned a transient processing status, while the worker used checkpoint and DLQ data internally.

Reason: a useful enterprise read-only tool needs a real read model with tenant isolation. The Agent should query task status through a stable store, while API and worker update the store at lifecycle boundaries.

Act: added `TaskStatusStore`, memory and Redis implementations, upload queued writes, worker processing/completed/failed writes, and the `etl_task_status` Agent tool.

Refine: added tests for tenant-scoped status persistence, upload status recording, worker status transitions, Agent tool execution, cross-tenant not-found behavior, and LLM Planner selection of `etl_task_status`.

## 2026-07-04T16:45:10+08:00 - Module 3 CI and E2E Hardening

Perceive: remote deterministic eval stalled after the Agent Planner work. Local smoke showed `etl-worker` was restarting because base config validation required Agent Planner credentials even though only `query-api` owns Agent planning.

Reason: enterprise services should validate only the capabilities they actually host. Worker startup must not depend on Agent Planner configuration; Query API still must reject invalid Agent Planner configuration. Agent audit state also needs completed tool steps, not long-lived `running` step states.

Act: moved Agent-specific validation behind `ValidateAPI()`, exposed Agent config through `docker-compose.yml` for `query-api`, and changed successful tool steps to persist as `completed` while the run continues.

Refine: verified full Go tests, Docker Compose config, RAG smoke, `rag_query` Agent HTTP E2E, and `etl_task_status` Agent HTTP E2E with an OpenAI-compatible planner mock.

## 2026-07-04T12:07:08+08:00 - Module 3 LLM Planner Integration

Perceive: the Agent API could run end-to-end, but its planner was still a fixed rule path. The user wanted a real enterprise-style LLM Planner while keeping deterministic dev/test fallback.

Reason: enterprise Agent systems should let the model propose the next action but never let it bypass local tool contracts. Keeping RulePlanner is valuable for CI, local development, and smoke tests, but it must stay minimal and be rejected in production.

Act: added an OpenAI-compatible `LLMPlanner`, exported Registry tool definitions, enforced structured JSON decisions, rejected unregistered tools and invalid arguments, added planner configuration, and made `auto` resolve to rule in dev and llm outside dev.

Refine: added tests for valid tool calls, final decisions, invalid model outputs, planner selection, and production planner validation. Remaining work is connecting more real tools and adding an external approval/audit service.

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
