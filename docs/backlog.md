# Backlog

最后核验：2026-09-08。这里只保留当前未完成事项、外部决策门和可执行的验收条件。
仓库规则维护：已将 `AGENTS.md` 的自然语言指令统一为中文，并明确简洁回答与最小上下文原则。
已完成的阶段计划、逐次 PRAR 记录和旧路线图保存在
[`docs/archive/backlog-2026-09-05.md`](archive/backlog-2026-09-05.md)。

## Active work

| ID | 当前事项 | 状态 | 下一步/完成条件 | 依据 |
| --- | --- | --- | --- | --- |
| P1.9 | 业务 Gold 晋级 | blocked | 由授权业务责任人提供签名 artifact、完整 manifest 和审批证据；随后运行 Gold finalize 与三次正式安全门禁 | 归档快照 P1.9 章节 |
| P2.3 | generation retention 生产启用 | pending decision | 批准 `INDEX_RETENTION_WINDOW`、保留期和部署责任；启用前完成 `index-generation-acceptance` | [`docs/index-generation-acceptance.md`](index-generation-acceptance.md) |
| P2.5-PROD | 企业身份生产方案 | blocked | 选择 IdP 和 connector owner，确认 subject/default-role/撤权/保留/SLA/轮换策略 | [`docs/enterprise-identity-decision-register.md`](enterprise-identity-decision-register.md) |
| P2.5-STAGE | 真实身份 staging 验收 | blocked | 完成 SCIM create/update/deactivate/reactivate、OIDC 登录、并发重放、标识复用和密钥轮换验收，保留签名 evidence bundle | [`docs/enterprise-identity-production-acceptance.md`](enterprise-identity-production-acceptance.md) |
| P2.6 | 生产证据门 | blocked | 由业务和运维批准 SLO、RPO/RTO、容量、质量、备份恢复、驻留和预算，再执行保留证据的验收 | 归档快照 P2.6 规划章节 |

以上事项保留在 backlog 中用于生产准入跟踪，但不属于当前 Agent 审计功能路线；除非用户重新指定，不得自动启动。

## Current execution plan

当前唯一主线是“企业级 Agent 审计功能完善”，不是通用生产准入或 OCR 验收。由于目标
已从固定流程中的模型审查器调整为受约束的自主预审 Agent，并已将方案收缩为最小
自主闭环。新会话按以下顺序推进：

1. **P2.4-R1-MVP（代码完成）**——已复用现有 Orchestrator，实现 Review Planner、四个只读工具、最小证据校验、发布中心接入和累计 token 预算控制。
2. **P2.4-R1-ACCEPT（已完成，2026-09-08）**——确定性发布矩阵、真实预算终止、Review Agent 部署恢复和真实模型四场景验收已通过。
3. **P2.4-R5：企业级编排（当前阶段）**——已落地审批通知 outbox 与反向补偿；外部工作流引擎适配后置。
4. **P2.4-R3：Review report 生命周期**（后置）——最后处理 TTL、过期清理和自动重审。

当前路线的完成标准是：权限边界、审计证据、失败处理、幂等性、租户隔离和对应业务验收矩阵均已覆盖。不得把当前已实现的受限规则扫描描述为完整合规审查。

### Agent 预审剩余工作排序（按整体功能完整度）

1. **累计 token 预算控制**（已完成，2026-09-08）：按整个 Review Run 累计模型 token，超限时持久化失败并转人工；真实模型专项验收已通过，证据保存在 `artifacts/release-center-token-budget-acceptance/`。
2. **预审恢复与幂等专项验收**（已完成，2026-09-08）：隔离栈 `kill query-api` + `redis-state` 停止/恢复后，同一 Review Run 继续、首个工具幂等键不变、四个只读工具不重复执行；证据保存在 `artifacts/release-center-review-recovery-acceptance/`。
3. **真实模型四场景验收**（已完成，2026-09-08）：普通、敏感信息、提示词注入、证据不足已通过真实模型隔离栈验收；证据保存在 `artifacts/release-center-real-model-scenarios-acceptance/`。
4. **Review Report 生命周期**（中到大）：实现 TTL、过期状态、候选变化重审、清理调度和历史证据保留。
5. **完整语义/隐私/合规审查**（大）：扩展政策库、结构化分类、版本差异和跨文档冲突审查。
6. **通知、Saga 与外部工作流**（进行中）：审批通知 outbox 与反向补偿已交付；外部 BPM/工作流引擎适配仍后置。

## Agent pre-review remaining

| ID | 当前事项 | 状态 | 下一步/完成条件 | 依据 |
| --- | --- | --- | --- | --- |
| P2.4-R2 | 企业审批组与可配置策略 | completed | 已交付租户隔离审批组、成员启停、空间/权限/风险匹配、优先级、双人审批、自审控制、策略持久化和管理 API；企业 IdP 同步、委托和定时升级另行立项 | [`docs/product-requirements-document.md`](product-requirements-document.md) |
| P2.4-R1 | 自主预审 Agent | completed | 最小自主闭环、累计 token 预算、真实预算终止、部署恢复和真实模型四场景验收已完成 | [`docs/agent-pre-review-architecture-and-implementation-plan.md`](agent-pre-review-architecture-and-implementation-plan.md) |
| P2.4-R5 | Agent 周边编排能力 | in progress | 审批通知 outbox、失败重试和反向补偿已交付；外部工作流引擎仍使用 webhook/`Decide` 契约，不内嵌引擎 | [`docs/agent-orchestration-r5-design.md`](agent-orchestration-r5-design.md) |
| P2.4-R3 | Review report 生命周期 | deferred | 暂不实施；待 R1、R5 完成后再决定报告 TTL、过期状态、清理/重审调度和历史审计保留策略 | [`services/etl-worker/internal/releasecenter/releasecenter.go`](../services/etl-worker/internal/releasecenter/releasecenter.go) |
| P2.4-R4 | `success` 状态数据库兼容 | completed | 已将 `success`（含大小写变体）规范化为 PostgreSQL 允许的 `completed`，并补充协调器回归测试；后续真实 PG 验收随发布中心矩阵执行 | [`services/etl-worker/internal/releasecenter/coordinator.go`](../services/etl-worker/internal/releasecenter/coordinator.go) |

## Completed baseline

- P2.2 durable admission/outbox、消费者幂等和 crash recovery 已完成。
- P2.3 generation manifest、双索引校验、激活、回滚、修复和观测已完成；仅生产 retention 仍需决策。
- P2.4 version-bound publication、exact-candidate approval、替换、可恢复删除和治理验收已完成。
- P2.5-A～D 及个人演示 F1～F6 已实现；企业生产启用仍受外部输入约束。
- Knowledge Release Center 的发布策略、审批组/策略、HTTP 矩阵和 Web 工作台已完成；自主 Agent 预审闭环、真实模型四场景和部署恢复验收已完成；审批组与策略管理当前通过管理员 HTTP API 提供。
- 583 页扫描 PDF 已由用户独立完成验证，不再作为当前 backlog 事项。
- 上述完成仅指首个版本的受限发布资格预审；P2.4-R1 MVP 已关闭。P2.4-R5 正在交付通知与补偿，P2.4-R3 仍后置。不得将当前 Agent 预审描述为完整合规审查。
- Elasticsearch 只读锁恢复、文档管理优化、OCR 分页恢复和相关反馈项已在历史验收中关闭；若运行环境再次出现，应创建新的带证据条目。

## Acceptance gates

- 发布中心变更必须执行 [`docs/release-center-functional-acceptance.md`](release-center-functional-acceptance.md) 要求的快速矩阵和完整业务矩阵。
- 生产晋级必须同时有 Go/Python/Web、Compose、安全、确定性评估以及相应的真实模型、负载和恢复证据。
- 所有开放事项必须补充责任人、外部依赖、验收命令和最后核验日期后才能进入实施。

## Archive policy

- 完成的计划和结果移入 `docs/archive/`，原文保留，不在当前 backlog 重复维护。
- 历史条目中的旧状态按其日期理解；当前状态以本文件和对应设计/验收文档为准。
- 新增事项使用唯一 ID；关闭事项记录关闭日期和证据链接，不删除历史。
