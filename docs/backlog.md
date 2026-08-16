# Backlog

## 2026-08-15 - 企业级改造 Phase 1（已交付）

Status: implemented

把「面试 demo 属性」的平台产品化为企业级项目：真实登录认证、用户/租户/文档注册表（PostgreSQL）、文档管理 API、前端产品 UI。核心内容：

1. **PostgreSQL 注册表**：`tenants`/`users`/`documents` 表 + `internal/migrations`（embedded SQL 自动迁移）；upload/worker 写穿文档状态，删除级联写透。跨租户删除 bug 修复（Qdrant/ES 删除按 tenant 过滤）。
2. **真实认证**：`POST /v1/auth/login`（bcrypt → JWT），`BOOTSTRAP_ADMIN_*` 首启建初始 admin；admin 经 `/v1/users`、`/v1/tenants` 管理用户/租户。
3. **文档管理 API**：`GET /v1/documents`（分页/权限矩阵过滤/搜索）、`GET|DELETE /v1/documents/{id}`（租户隔离 404）。
4. **前端产品化**：登录页 + HttpOnly cookie 鉴权 + 侧边栏产品 UI（问答/文档管理/用户管理/数据接入/可观测/检索质量/Agent）。
5. **demo 资产清理**：面试文档归档至 `docs/archive/`，demo 脚本归档至 `scripts/archive/`，`seed-demo-data.py` → `load-corpus.py`（经登录灌语料）。

## 2026-08-15 - 企业级改造 Phase 2（已完成部分）

Status: implemented

1. **审计日志**：`audit_logs` 表（migration 0002）+ `internal/audit` 包（追加式 Store + List）；login（成功/失败/禁用）、upload、document delete 埋点（best-effort，不阻塞主操作）；`GET /v1/audit` admin 查看 API（租户隔离 + action 过滤）；前端 `/audit` 审计日志页（admin）。
2. **token 撤销**：`users.token_version` 列（migration 0003）+ `SetPasswordHash` 递增；JWT 携带 `token_version`，`Verifier` middleware 对 DB 中的用户校验版本/active（`NewVerifierWithStore`），密码重置后旧 token 失效。离线/测试 token（非 UUID UserID 或 DB 无行）放行，JWT 签名保护。
3. **租户内 admin 隔离**：admin 仅管理本租户用户——创建用户强制使用调用者 JWT 的 tenant（忽略请求体）；update/delete/重置密码经 `requireTenantUser` 校验，跨租户目标一律 404（不透漏存在性）。

## 2026-08-15 - 企业级改造 Phase 2（剩余）

Status: planned

0. **负例拒答兜底（实验完成，结论：分数门控不可行 → 生成后忠实度验证）**：负例（无权访问的文档）检索正确不透漏，但 LLM 未输出拒答句导致 `negative_case_missing_not_found_fallback` 失败。
   **实验结论（2026-08-16，bge-m3）**：
   - 离线 pairwise：正负分布重叠宽度 **0.2271**（nomic 时 0.2784），绝对阈值无干净分离点；
   - 在线真实 eval（独立栈，bge-m3 + deepseek，44 语义集）：**26/38 正例命中时 max_relevance 恰好 0.5**（strict_rank=1，dense cosine 真实值），唯一失败的负例 sem-014 候选最高分也是 0.5 → 正负分数完全重合，`RETRIEVAL_MIN_RELEVANCE` 硬门控不可用（任何阈值都误杀正例，ADR 0006 重测结论维持）；
   - 现状：6 个负例中 **5 个已由 deepseek 遵循 prompt 拒答**，仅 sem-014 失败——其 query 是权限机制提问（非机密内容提问），检索返回 5 个弱相关公开候选（max_rel=0.5），LLM 误判有据而编答。
   **根治方向（2026 企业级实践：生成后忠实度验证为主导机制，阈值不是主导）**：
   1. query-api 生成后校验答案声明的可溯源性（LLM-based grounding check，或确定性词/数字级验证），验证不过 → 固定拒答句「未找到相关文档，无法回答该问题。」；sem-014 的编造声明无法从弱相关候选溯源，正例可从目标文档溯源；
   2. mock 负例用确定性候选相关性规则（mock hash embedding 有区分度，无高相关候选 → 拒答）；
   3. 验证决策进 `retrieval_info` + 报告，保持可观测；
   4. 回归验证：真实负例拒答 100% + 正例 Recall@1 不降（`--min-hit-rate 0.90` 把关检索）。
   验收：mock + real 的负例拒答率 100%，且正例 Recall@1 不降。
   **已实施（2026-08-16，real 达成，mock 待）**：
   - query-api 生成后 grounding check（`RETRIEVAL_GROUNDING_CHECK` 默认 true，模糊带 0.45–0.70）：答案生成后由校验器 LLM 判定答案关键断言（事实/数字/结论）是否可溯源到检索来源，判 false → 固定拒答句；决策进 `retrieval_info.grounding_checked/grounding_passed`。真实验证正例 28/28 通过、无误伤。
   - sem-014 根因确认：其 query 问权限机制，被 user **合法可见**的 internal 规则文档（sem-006「三个可见级别」）合理回答（答案逐字复述 sem-006），grounding 判 supported 正确 → **评测数据缺陷非系统缺陷**。已把 query 改为「项目机密资料涉及的项目成员名单是什么？」（指向机密实体，与可见文档 max 相似度 0.429，避开 sem-006）。
   - 验证（bge-m3 语义集）：6/6 负例拒答（含修正后 sem-014），正例 hit_rate 0.921 不降；剩余 3 失败（sem-002/013/033）为既有 `retrieval:timeout` 检索噪声。
   - mock 验证（2026-08-16）：mock 语义集负例 6/6 仍失败，且 **确定性候选规则对 mock 不可行**——mock 的 hash relevance 在 0.5 处饱和（正例 median 与负例 5/6 均恰为 0.5，无分离阈值），mock LLM 输出固定模板（「基于参考文档回答」）不遵循拒答。mock 负例拒答属 mock 语义能力边界（mock 仅验链路，CLAUDE.md 明示其指标非质量证据），不作为质量门禁；CI 门禁（golden-set 锚点集，--min-hit-rate/pass-rate/answer-pass-rate 0.90）不受影响。
1. **文档搜索/详情深化（已交付 2026-08-16）**：
   - `GET /v1/documents/search?q=`：ES BM25 全文检索聚合到文档级（tenant+permission 过滤，docstore 富化 file_name/status，bestScore 排序；ES 关闭 → 503）。
   - `GET /v1/documents/{docID}/chunks`：Qdrant scroll 按 doc+tenant+permission 返回全部切块（content/index/metadata，按 index 排序）；权限复用 `permissionAllowed`（跨租户/无权限 404）。store 层 `QdrantStorer.ListChunksByDoc`。
   - 前端：文档详情页 `/documents/[id]`（元数据卡片 + chunks 手风琴，复用问答页引用展开模式）；列表页内容检索框 + 详情链接；apiClient/types/代理路由。
   - 顺带修复：`parseUploadMetadata` 空 metadata 返回 nil → PG NOT NULL 违反（任何不带 metadata 的上传都会失败）→ 返回空 map。
   - 测试：store 3 + handler 8 单测、web build、e2e-smoke（search/chunks 断言）全通过。
   - e2e-smoke 加固：改为独立 compose project `ai-etl-smoke` + 独立端口（eval override 隐藏后端端口 + smoke override 仅暴露 query-api:8081）+ BOOTSTRAP admin 建在上传同租户。**原因：cleanup `down -v` 曾删掉共享主栈 volumes（数据丢失）**。
2. **前端硬化**：HttpOnly cookie 复核、`/v1/query` retrieval 元数据进文档详情。

## 2026-08-08 - AI Engineer Roadmap

Status: planned

Goal: 把项目从「带 RAG 外壳的分布式后端」改造成「能量化讲清 AI 质量的 AI 应用工程项目」。

完整计划、优先级、跟进表见 `docs/ai-engineer-roadmap.md`。摘要：

1. P0 建立可信评测基线：当前 100% 的评测结果基于 8 维哈希 mock embedding 与锚点式 golden set，不构成质量证据。需支持真实模型、重建语义数据集、产出对照实验、补 token/成本可观测。
2. P1 修确定性缺陷：中文 token 估算偏差 2-4 倍、topK 无上限、主查询链路缺 prompt injection 防护、默认模型过旧、仓库残留文件。
3. P2 补 AI 侧短板：SSE 流式 + TTFT、演示前端、prompt 版本管理、Agent 评测与 native tool calling。
4. P3 架构改进：Redis 单实例混装 durable 状态与 LRU 缓存（正确性问题，阶段 1 提到 P1）、审批审计 24h TTL 后消失、无文档删除与重建索引、双写无对账、query-api 单体承载查询与 Agent 编排。
5. P4 面试叙事：基于真实数据重写讲解材料，补选型 ADR。

## 2026-07-13 - Module 4 Engineering Learning Evaluation

Status: implemented and executed

Goal: provide a repeatable 100-case learning and regression evaluation without misrepresenting synthetic architecture cases as enterprise historical evidence.

Plan:

1. Maintain 100 deidentified synthetic cases across ETL, retrieval, Agent, and observability, including permission-refusal negatives.
2. Validate the set for structure, reference answers, and high-confidence sensitive patterns.
3. Run it in a unique Compose project with dynamic host ports so cleanup cannot affect the default development stack.
4. Require retrieval, answer, and final assertions for positive and negative cases.
5. Keep production acceptance separate: use the ignored, reviewer-approved private historical set before making user-quality or Judge claims.

## 2026-07-13 - Module 4 LLM Alerting and Dashboard

Status: implemented

Goal: detect LLM dependency degradation and deliver actionable enterprise alerts without inventing streaming metrics for a non-streaming API.

Plan:

1. Add low-cardinality LLM request outcome, latency, and process-local consecutive failure metrics.
2. Route LLM calls through the existing circuit breaker and classify bounded failure outcomes.
3. Add Prometheus alerts for five consecutive failures, rolling error rate, and p95 latency.
4. Add rule tests proving failure streak trigger and success reset behavior.
5. Add Alertmanager grouping, critical routing, inhibition, and authenticated webhook delivery.
6. Add a secret-backed WeCom/DingTalk adapter with DingTalk HMAC signing.
7. Provision a Grafana overview dashboard for Query, LLM, circuit, and Agent signals.
8. Add observability validation to required CI checks.
9. Preserve non-root runtime identities when local Compose secret bind mounts retain host ownership.
10. Validate private historical evaluation datasets for case count, reference answers, structural integrity, and high-confidence sensitive patterns before Judge execution.

## 2026-07-13 - Module 4 Optional LLM-as-a-Judge

Status: implemented

Goal: add a structured, optional answer-quality judge without weakening deterministic CI gates.

Plan:

1. Define strict structured scores for faithfulness, correctness, relevance, overall pass, reasons, and unsupported claims.
2. Add an OpenAI-compatible Judge client with bounded retries and response validation.
3. Run Judge scoring while Query answers and retrieved contexts are still in memory.
4. Add per-case Judge results, aggregate scores, thresholds, and errors to JSON/Markdown reports.
5. Keep Judge disabled by default and preserve deterministic retrieval/answer assertions as required CI checks.
6. Add a manual GitHub Actions workflow using `JUDGE_API_KEY` and report artifacts.
7. Test schema requests, retries, invalid scores, mock HTTP compatibility, and report generation.

## 2026-07-13 - Module 4 Query Trace Topology

Status: implemented

Goal: make a Query request traceable from the HTTP boundary through retrieval stages and final LLM generation.

Plan:

1. Continue incoming W3C `traceparent` headers and return `X-Trace-ID` to API callers.
2. Propagate trace context to embedding, Qdrant, Elasticsearch, reranker, and LLM HTTP requests.
3. Add Query, prompt construction, LLM generation, retrieval, cache, route, backend search, fusion, and rerank spans.
4. Expose `X-Trace-ID` to browser clients through CORS.
5. Add tests for HTTP trace propagation and retrieval-stage span topology.
6. Verify focused packages and the full Go suite before completion.

## 2026-07-05 - Module 3 Agent Observability and Alerts

Status: implemented

Goal: make Agent orchestration visible through low-cardinality metrics and production alert rules.

Plan:

1. Add Agent run creation metrics with `auto_execute` label.
2. Add terminal run completion and duration metrics with `state` and normalized `error_type` labels.
3. Add tool-step count and duration metrics with `tool_name` and `state` labels.
4. Add durable approval-decision metrics with `decision` and `tool_name` labels.
5. Wire Agent API lifecycle mutations into a small observer interface instead of coupling Agent API directly to Prometheus.
6. Emit events only when a run or step changes state, avoiding duplicate counts from reads or repeated requests.
7. Add Prometheus alerts for Agent failures, lifecycle timeouts, and high p95 run duration.
8. Cover metrics and Agent API observer behavior with tests.

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
