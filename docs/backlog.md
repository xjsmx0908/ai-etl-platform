# Backlog

最后核验：2026-09-05。这里只保留当前未完成事项、外部决策门和可执行的验收条件。
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

当前唯一主线是“企业级 Agent 审计功能完善”，不是通用生产准入或 OCR 验收。新会话必须按以下顺序推进：

1. **P2.4-R1：完整语义、隐私和合规审查**——定义模型输入、结构化输出、chunk 证据、提示词注入处理、fail-closed 和模型评测。
2. **P2.4-R5：企业级编排**——补齐异步审批通知、失败补偿、Saga 和外部工作流接入。
3. **P2.4-R3：Review report 生命周期**（后置）——最后处理 TTL、过期清理和自动重审。

当前路线的完成标准是：权限边界、审计证据、失败处理、幂等性、租户隔离和对应业务验收矩阵均已覆盖。不得把当前已实现的受限规则扫描描述为完整合规审查。

## Agent pre-review remaining

| ID | 当前事项 | 状态 | 下一步/完成条件 | 依据 |
| --- | --- | --- | --- | --- |
| P2.4-R2 | 企业审批组与可配置策略 | completed | 已交付租户隔离审批组、成员启停、空间/权限/风险匹配、优先级、双人审批、自审控制、策略持久化和管理 API；企业 IdP 同步、委托和定时升级另行立项 | [`docs/product-requirements-document.md`](product-requirements-document.md) |
| P2.4-R1 | 完整语义、隐私和合规审查 | next | 定义文档内容输入、结构化输出、证据引用、提示词注入处理和 fail-closed 策略；补充模型评测与业务验收 | [`docs/product-requirements-document.md`](product-requirements-document.md) |
| P2.4-R5 | Agent 周边编排能力 | planned | 评估多工具 Saga、异步审批通知和外部工作流引擎；每项定义可靠性、审计和失败补偿验收 | [`docs/agent-orchestrator-design.md`](agent-orchestrator-design.md) |
| P2.4-R3 | Review report 生命周期 | deferred | 暂不实施；待 R1、R5 完成后再决定报告 TTL、过期状态、清理/重审调度和历史审计保留策略 | [`services/etl-worker/internal/releasecenter/releasecenter.go`](../services/etl-worker/internal/releasecenter/releasecenter.go) |
| P2.4-R4 | `success` 状态数据库兼容 | completed | 已将 `success`（含大小写变体）规范化为 PostgreSQL 允许的 `completed`，并补充协调器回归测试；后续真实 PG 验收随发布中心矩阵执行 | [`services/etl-worker/internal/releasecenter/coordinator.go`](../services/etl-worker/internal/releasecenter/coordinator.go) |

## Completed baseline

- P2.2 durable admission/outbox、消费者幂等和 crash recovery 已完成。
- P2.3 generation manifest、双索引校验、激活、回滚、修复和观测已完成；仅生产 retention 仍需决策。
- P2.4 version-bound publication、exact-candidate approval、替换、可恢复删除和治理验收已完成。
- P2.5-A～D 及个人演示 F1～F6 已实现；企业生产启用仍受外部输入约束。
- Knowledge Release Center 的发布策略、Agent 预审、内容 findings、审批组/策略、HTTP 矩阵和 Web 工作台已完成；审批组与策略管理当前通过管理员 HTTP API 提供。
- 583 页扫描 PDF 已由用户独立完成验证，不再作为当前 backlog 事项。
- 上述完成仅指首个版本的受限发布资格预审；P2.4-R1、P2.4-R3、P2.4-R5 仍是未完成、未来范围或待修复事项，不得将当前规则扫描描述为完整合规审查。
- Elasticsearch 只读锁恢复、文档管理优化、OCR 分页恢复和相关反馈项已在历史验收中关闭；若运行环境再次出现，应创建新的带证据条目。

## Acceptance gates

- 发布中心变更必须执行 [`docs/release-center-functional-acceptance.md`](release-center-functional-acceptance.md) 要求的快速矩阵和完整业务矩阵。
- 生产晋级必须同时有 Go/Python/Web、Compose、安全、确定性评估以及相应的真实模型、负载和恢复证据。
- 所有开放事项必须补充责任人、外部依赖、验收命令和最后核验日期后才能进入实施。

## Archive policy

- 完成的计划和结果移入 `docs/archive/`，原文保留，不在当前 backlog 重复维护。
- 历史条目中的旧状态按其日期理解；当前状态以本文件和对应设计/验收文档为准。
- 新增事项使用唯一 ID；关闭事项记录关闭日期和证据链接，不删除历史。
