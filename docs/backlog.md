# Backlog

## 2026-07-04 - Module 3 Agent Run Lifecycle Governance

Status: implemented

Goal: make Agent runs operationally controllable after creation, especially for hung executions and stale human approvals.

Plan:

1. Add explicit run cancellation with tenant isolation and owner-or-approver authorization.
2. Persist cancellation metadata: cancelled by, cancel reason, and cancelled at.
3. Mark the active step as cancelled when a non-terminal run is cancelled.
4. Add run-level timeout enforcement before planning or tool recovery.
5. Add approval timeout enforcement for runs stuck in `pending_approval`.
6. Reject pending approval audit records when a lifecycle timeout or cancellation closes the run.
7. Prevent terminal runs from being resumed or cancelled through the HTTP API.
8. Expose `POST /v1/agent/runs/{id}/cancel`.
9. Add `AGENT_RUN_TIMEOUT` and `AGENT_APPROVAL_TIMEOUT` configuration and Compose/env templates.
10. Cover cancel, timeout, authorization, tenant isolation, terminal conflict, and approval-audit synchronization with tests.

## 2026-07-04 - Module 3 Redis Distributed Lock

Status: implemented

Goal: make the Agent Orchestrator safe for multi-replica `query-api` deployments.

Plan:

1. Add a Redis-backed `LockManager` for Agent runs.
2. Store short-lived lock ownership separately from a longer-lived monotonic fencing token.
3. Reject any non-expired lock acquisition, including same-owner reacquire; use `Extend` for lease renewal.
4. Keep stale owner release from deleting a newer lease.
5. Preserve fencing token growth across release and reacquire so stale writes remain rejected by the store layer.
6. Use `MemoryLockManager` in dev/test and `RedisLockManager` outside dev.
7. Close Redis lock resources together with Agent run and approval stores.
8. Add lock tests for competing owners, same-owner reacquire rejection, lease extension, stale release, and optional real Redis contract verification.

## 2026-07-04 - Module 3 External Approval and Audit

Status: implemented

Goal: replace request-local approval with durable, tenant-scoped approval records for side-effecting Agent tools.

Plan:

1. Add `ApprovalRequest` and `ApprovalStore` with in-memory and Redis-backed implementations.
2. Use deterministic per-run-step approval ids so pending approval creation is idempotent.
3. Create an approval record whenever an Agent run reaches `pending_approval`.
4. Add `GET /v1/agent/runs/{id}/approvals`, `POST /v1/agent/runs/{id}/approve`, and `POST /v1/agent/runs/{id}/reject`.
5. Require `agent:approve` for approve/reject while keeping approval listing tenant-scoped.
6. Persist approval decisions with approver, decision time, and reason.
7. Resume approved runs from the durable approval record so execution can recover after request interruption.
8. Add rejection handling that marks the pending tool step and run as failed without executing the tool.
9. Cover store isolation, approve, reject, tenant isolation, and approved-record resume behavior with tests.

## 2026-07-04 - Module 3 Task Status Tool

Status: implemented

Goal: add the second real read-only Agent tool and give it a real task-status read model instead of a fake lookup.

Plan:

1. Add `model.TaskStatus` and `model.TaskStatusStore`.
2. Add `internal/taskstatus` with in-memory and Redis-backed stores.
3. Add `TASK_STATUS_STORE` and `TASK_STATUS_TTL` configuration and environment templates.
4. Save `queued` status from `/v1/upload` before publishing to Kafka.
5. Save `processing`, `completed`, and `failed` statuses from the ETL worker pipeline.
6. Register `etl_task_status` in the Agent Registry with schema validation and `agent` permission.
7. Keep task lookup tenant-scoped so cross-tenant task ids return `not_found`.
8. Add tests for the status store, upload status write, worker status transitions, Agent tool execution, tenant isolation, and LLM Planner selection of the new tool.

## 2026-07-04 - Module 3 LLM Planner Integration

Status: implemented

Goal: make the Agent Orchestrator use a real model-backed planner outside development while preserving deterministic dev/test execution.

Plan:

1. Add `LLMPlanner` backed by an OpenAI-compatible chat-completions endpoint.
2. Require planner output to be a structured JSON decision: `tool_call` or `final`.
3. Expose Registry tool definitions to the planner without exposing handlers.
4. Reject unregistered tools, invalid decision JSON, invalid tool arguments, and empty final answers before the orchestrator executes anything.
5. Add `AGENT_PLANNER_TYPE=auto|llm|rule`; resolve `auto` to `rule` in dev and `llm` outside dev.
6. Add LLM planner endpoint, API key, model, timeout, and max-token configuration.
7. Reject `AGENT_PLANNER_TYPE=rule` in production to keep RulePlanner as dev/test fallback only.
8. Add tests for LLM tool-call planning, final planning, guardrail failures, planner selection, and production config validation.

## 2026-07-04 - Module 3 CI and E2E Hardening

Status: implemented

Goal: unblock CI/deterministic eval and prove the Agent API works through real HTTP boundaries.

Plan:

1. Move Agent Planner validation from shared worker/API config validation into `ValidateAPI()`.
2. Keep production Query API guardrails for LLM Planner while allowing worker startup without Agent Planner credentials.
3. Expose Agent runtime/planner environment variables in `docker-compose.yml` for `query-api`.
4. Persist successful tool-call steps as `completed` for correct audit semantics.
5. Verify full Go tests, compose config, RAG smoke, `rag_query` Agent HTTP E2E, and `etl_task_status` Agent HTTP E2E.

## 2026-07-04 - Module 3 Agent API Integration

Status: implemented

Goal: expose the stateful Agent Orchestrator through a minimal HTTP API and connect it to one real platform capability.

Plan:

1. Export `query.Service.Ask` so tools can reuse the existing retrieval and generation pipeline in-process.
2. Add authenticated actor propagation for user id, role, and scopes.
3. Add `internal/agentapi` with `POST /v1/agent/runs`, `GET /v1/agent/runs/{id}`, `POST /v1/agent/runs/{id}/resume`, and `POST /v1/agent/runs/{id}/approve`.
4. Register `rag_query` as the first real read-only Agent tool backed by Query Service.
5. Add a deterministic first planner that calls `rag_query` once and finalizes from the tool result.
6. Add Agent configuration for node id, max steps, lock TTL, and run TTL.
7. Normalize Agent API metric labels to avoid high-cardinality run ids.
8. Add tests for RAG-backed run creation/execution, run lookup, approval, and config defaults/overrides.

## 2026-07-04 - Module 3 Agent Orchestrator MVP

Status: implemented as internal core package

Goal: introduce the first stateful Agent Orchestrator slice without coupling it to HTTP or a real LLM planner yet.

Plan:

1. Add `internal/agent` with durable `Run`, `Step`, state, planner, tool, store, lock, and policy types.
2. Add a strict Tool Registry that validates JSON arguments before invoking handlers.
3. Add RBAC-style tool authorization with required permissions and approval checks.
4. Add an Orchestrator that acquires a per-run lock, persists state transitions, executes tools, and enforces max-step failure.
5. Add in-memory Store and LockManager implementations for deterministic tests.
6. Add version and fencing-token checks to reject stale writes from expired owners or concurrent writers.
7. Add a Redis-backed Agent Store for process-independent run persistence.
8. Add waiting-tool recovery so persisted tool calls resume with the same idempotency key instead of asking the planner for a new action.
9. Add pending approval, side-effect registration guardrails, and compensation handlers for failed side-effecting tools.
10. Add tests for schema validation, authorization, idempotency key generation, state persistence, recovery, max-step guardrails, lock takeover, stale lock rejection, stale write rejection, approval, and compensation.
11. Document the MVP boundaries and next integration steps in `docs/agent-orchestrator-design.md`.

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

## 2026-06-29 - Module 2 Lightweight Load Test

Status: implemented

Goal: produce a lightweight performance baseline for the completed Hybrid Retrieval & Reranking Engine.

Plan:

1. Extend the query load-test script to report scenario name, throughput, top-hit rate, and business metadata exact-match cases.
2. Run cache-miss rerank-off, cache-miss rerank-on, cache-hit rerank-on, and schema exact rerank-on scenarios with low concurrency.
3. Use the benchmark to verify latency impact from CPU Cross-Encoder reranking and latency reduction from Redis semantic cache.
4. Fix the exact-route guardrail gap found during schema exact pressure testing.
5. Document the benchmark result in `docs/module2-load-test-report.md`.
