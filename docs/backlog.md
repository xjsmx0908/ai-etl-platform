# Backlog

最后核验：2026-09-10。这里只保留当前未完成事项、外部决策门和可执行的验收条件。
仓库规则维护：已将 `AGENTS.md` 的自然语言指令统一为中文，并明确简洁回答与最小上下文原则。
已完成的阶段计划和历史实验报告已移出工作树；当前状态以本文件和对应设计/验收文档为准。

## Active work

| ID | 当前事项 | 状态 | 下一步/完成条件 | 依据 |
| --- | --- | --- | --- | --- |
| P1.9 | 业务 Gold 晋级 | blocked | 由授权业务责任人提供签名 artifact、完整 manifest 和审批证据；随后运行 Gold finalize 与三次正式安全门禁 | 归档快照 P1.9 章节 |
| P2.3 | generation retention 生产启用 | pending decision | 批准 `INDEX_RETENTION_WINDOW`、保留期和部署责任；启用前完成 `index-generation-acceptance` | [`docs/index-generation-acceptance.md`](index-generation-acceptance.md) |
| P2.5-PROD | 企业身份生产方案 | blocked | 选择 IdP 和 connector owner，确认 subject/default-role/撤权/保留/SLA/轮换策略 | [`docs/enterprise-identity-decision-register.md`](enterprise-identity-decision-register.md) |
| P2.5-STAGE | 真实身份 staging 验收 | blocked | 完成 SCIM create/update/deactivate/reactivate、OIDC 登录、并发重放、标识复用和密钥轮换验收，保留签名 evidence bundle | [`docs/enterprise-identity-production-acceptance.md`](enterprise-identity-production-acceptance.md) |
| P2.6 | 生产证据门 | blocked | 由业务和运维批准 SLO、RPO/RTO、容量、质量、备份恢复、驻留和预算，再执行保留证据的验收 | 归档快照 P2.6 规划章节 |

以上事项保留在 backlog 中用于生产准入跟踪，但不属于当前 Agent 审计功能路线；除非用户重新指定，不得自动启动。


## Real-model and ingestion capacity

与 L1 mock 查询门禁分开。本机演示栈继续运行时，只允许串行入库测量；真实 RAG 质量评测需要停演示栈或换机器。数字只作为容量包络，不写入 ADR 0010，不加入 Required Checks。

| ID | 当前事项 | 状态 | 下一步/完成条件 | 依据 |
| --- | --- | --- | --- | --- |
| P-CAP-1 | 入库容量包络 | done | 脚本与契约测试已落地。演示栈 CPU bge-m3：short-text 2 chunk / 11s，typical-doc 12 chunk / 24s。扫描 PDF 仍用 `--fixture-file` | [`ingestion-capacity.md`](ingestion-capacity.md) |
| P-CAP-2 | 真实模型 RAG 质量 | blocked | 停演示栈或换机器后运行 `python3 scripts/run-evals.py --real-models --embed-dim 1024` | [`evals/README.md`](evals/README.md) |
| P-CAP-3 | 真实问答时延观察 | pending | 入库完成后串行 5–10 次完整 `/v1/query`，不回写 L1 阈值 | [`ingestion-capacity.md`](ingestion-capacity.md) |
| P-CAP-5 | 问答真流式 | done | SSE 在 grounding 前推送 token；校验失败发 `replace`；`done.answer` 为最终答案 | 本文件 |
| P-CAP-4 | 入库阶段耗时展示 | done | 任务/文档返回 `stage_timings`；上传页和文档页展示；Grafana Parse/Embed/Store/OCR p95。演示栈短文本 parse 4ms / embed 7.9s / store 314ms / total 8.2s | 本文件 |
| P-CAP-6 | 本地 embedding keep-alive | done | Ollama `keep_alive=24h` + worker/query-api 启动预热已重建进演示栈。短文本 embed 7.9s→487ms，HTTP 202 为 28ms，就绪 2.0s。问答检索 350–430ms，首字 3–5s 是远程 LLM | 本文件 |

## Product experience UAT

| ID | 当前事项 | 状态 | 下一步/完成条件 | 依据 |
| --- | --- | --- | --- | --- |
| P-UAT-1 | 产品体验验收 | done | UAT-007/010/011/012/013 与 PX-06/PX-09 已复验。登记册开放项已清空 | [`docs/product-experience-acceptance.md`](product-experience-acceptance.md) |
| P-UAT-1-S1 | 只读用户问答失败 | done | 2026-09-09 只读可问「赴港流程」；空间下拉可见。UAT-010 已修复 | [`../issues/findings-register.md`](../issues/findings-register.md) |
| P-UAT-1-next | 剩余体验缺陷 | done | UAT-013 已复验：错密登录显示「用户名或密码错误」 | [`../issues/findings-register.md`](../issues/findings-register.md) |
| P-UAT-2 | 受管换版切块未切换 | done | UAT-014 已复验：新版本发布后问答 120 元，旧 generation 退役 | [`../issues/findings-register.md`](../issues/findings-register.md) |
| P-UAT-3 | 发布中心 Fail to fetch 红字 | in progress | 代码已改：静默轮询不展示瞬时 fetch 失败；待真实页面复验 UAT-015 | [`../issues/findings-register.md`](../issues/findings-register.md) |

这是 2026-09-09 明确要求的体验验收轨道，覆盖页面美观、使用逻辑和缺陷，不替代发布中心/治理/身份专项矩阵。问题登记以 [`issues/findings-register.md`](../issues/findings-register.md) 为准。

2026-09-09 执行记录：D0–D4 已回写。随后修复 UAT-010，并补 PX-06 机密双审页面证据与 PX-09 空状态；复验通过。矩阵 PX-02/PX-06/PX-09 改为通过。随后修复 UAT-011：问答 SSE 在 `event: done` 后结束。随后修复 UAT-012：只读上传按钮禁用并说明无权限。随后修复 UAT-007：web 不再用空 reports 目录覆盖烘焙 `latest.json`，`/quality` 展示 Recall@1 55%。随后修复 UAT-013：错密登录显示「用户名或密码错误」。登记册开放项已清空。

续跑入口已关闭：UAT-013 完成。2026-09-09-gap 覆盖补测后的 UAT-014 / P-UAT-2 已修复并复验，不重跑 D0–D4。

## Current execution plan

Agent 功能主线仍是“企业级 Agent 审计功能完善”，不是通用生产准入或 OCR 验收。并行进行 P-UAT-1 产品体验验收。由于目标
已从固定流程中的模型审查器调整为受约束的自主预审 Agent，并已将方案收缩为最小
自主闭环。新会话按以下顺序推进：

1. **P2.4-R1-MVP（代码完成）**——已复用现有 Orchestrator，实现 Review Planner、四个只读工具、最小证据校验、发布中心接入和累计 token 预算控制。
2. **P2.4-R1-ACCEPT（已完成，2026-09-08）**——确定性发布矩阵、真实预算终止、Review Agent 部署恢复和真实模型四场景验收已通过。
3. **P2.4-R5：企业级编排（已完成，2026-09-08）**——审批通知 outbox、企业微信/钉钉投递、外部工作流 webhook 与 callback 适配、反向补偿已交付；不内嵌 BPM 引擎。
4. **P2.4-R3：Review report 生命周期**（已完成，2026-09-08）——TTL、过期状态、自动重审和过期清理已交付。
5. **完整语义/隐私/合规审查**（后续项）——扩展政策库、版本差异和跨文档冲突审查；结构化材料分类不再作为发布开关。
6. **P2.4-R6 知识空间适配预审**（已完成）——空间用途 + 适不适合/能不能当知识用。

当前路线的完成标准是：权限边界、审计证据、失败处理、幂等性、租户隔离和对应业务验收矩阵均已覆盖。不得把当前已实现的受限规则扫描描述为完整合规审查。

### Agent 预审剩余工作排序（按整体功能完整度）

1. **累计 token 预算控制**（已完成，2026-09-08）：按整个 Review Run 累计模型 token，超限时持久化失败并转人工；真实模型专项验收已通过，证据保存在 `artifacts/release-center-token-budget-acceptance/`。
2. **预审恢复与幂等专项验收**（已完成，2026-09-08）：隔离栈 `kill query-api` + `redis-state` 停止/恢复后，同一 Review Run 继续、首个工具幂等键不变、四个只读工具不重复执行；证据保存在 `artifacts/release-center-review-recovery-acceptance/`。
3. **真实模型四场景验收**（已完成，2026-09-08）：普通、敏感信息、提示词注入、证据不足已通过真实模型隔离栈验收；证据保存在 `artifacts/release-center-real-model-scenarios-acceptance/`。
4. **通知、Saga 与外部工作流**（已完成，2026-09-08）：审批通知可投递到企业微信/钉钉，外部引擎可通过 webhook 收事件并用 callback 回写 Decide。
5. **Review Report 生命周期**（已完成，2026-09-08）：TTL 默认 7 天，过期后自动重审同一候选并保留旧报告；已发布/已拒绝证据不改写；超保留期且无引用的过期报告可清理。
6. **P2.4-R6 知识空间适配预审**（已完成，2026-09-08）：管理员为知识空间填写用途；预审判断是否适合该空间、能否作为正式知识，类型只作备注。
7. **P2.4-R6-ACCEPT 真实环境验收**（已完成，2026-09-08）：隔离栈 13 场景含用途匹配/不适配/非知识；真实模型七场景含第五个工具。聊天/草稿即使模型误判可发布，也会被服务端拦住。
8. **完整语义/隐私/合规审查**（大，暂缓）：扩展政策库、版本差异和跨文档冲突审查。不做材料类型分类器。

## Agent pre-review remaining

| ID | 当前事项 | 状态 | 下一步/完成条件 | 依据 |
| --- | --- | --- | --- | --- |
| P2.4-R2 | 企业审批组与可配置策略 | completed | 已交付租户隔离审批组、成员启停、空间/权限/风险匹配、优先级、双人审批、自审控制、策略持久化和管理 API；企业 IdP 同步、委托和定时升级另行立项 | [`docs/product-requirements-document.md`](product-requirements-document.md) |
| P2.4-R1 | 自主预审 Agent | completed | 最小自主闭环、累计 token 预算、真实预算终止、部署恢复和真实模型四场景验收已完成 | [`docs/agent-pre-review-architecture-and-implementation-plan.md`](agent-pre-review-architecture-and-implementation-plan.md) |
| P2.4-R5 | Agent 周边编排能力 | completed | 审批通知 outbox、企业微信/钉钉投递、外部工作流 webhook/callback 和反向补偿已交付；不内嵌 BPM 引擎 | [`docs/agent-orchestration-r5-design.md`](agent-orchestration-r5-design.md) |
| P2.4-R3 | Review report 生命周期 | completed | 已交付报告 TTL（默认 168h）、过期状态、同一候选自动重审、保留期清理（默认 90 天）和 `review_expired` 业务提示；`RELEASE_REVIEW_TTL=0` 可关闭过期 | [`services/etl-worker/internal/releasecenter/coordinator.go`](../services/etl-worker/internal/releasecenter/coordinator.go) |
| P2.4-R4 | `success` 状态数据库兼容 | completed | 已将 `success`（含大小写变体）规范化为 PostgreSQL 允许的 `completed`，并补充协调器回归测试；后续真实 PG 验收随发布中心矩阵执行 | [`services/etl-worker/internal/releasecenter/coordinator.go`](../services/etl-worker/internal/releasecenter/coordinator.go) |
| P2.4-R6 | 知识空间适配预审 | completed | 已交付空间用途、适配/知识可用性观察、类型备注和服务端发布下限；版本对比、政策库和冲突检查仍暂缓 | [`docs/agent-pre-review-architecture-and-implementation-plan.md`](agent-pre-review-architecture-and-implementation-plan.md) |
| P2.4-R6-ACCEPT | 知识空间适配真实环境验收 | completed | 隔离栈 13 场景通过；真实模型七场景含第五个工具通过；非正式材料服务端下限已验证 | [`docs/release-center-functional-acceptance.md`](release-center-functional-acceptance.md) |

## Completed baseline

- P2.2 durable admission/outbox、消费者幂等和 crash recovery 已完成。
- P2.3 generation manifest、双索引校验、激活、回滚、修复和观测已完成；仅生产 retention 仍需决策。
- P2.4 version-bound publication、exact-candidate approval、替换、可恢复删除和治理验收已完成。
- P2.5-A～D 及个人演示 F1～F6 已实现；企业生产启用仍受外部输入约束。
- Knowledge Release Center 的发布策略、审批组/策略、HTTP 矩阵和 Web 工作台已完成；自主 Agent 预审闭环、真实模型四场景和部署恢复验收已完成；审批组与策略管理当前通过管理员 HTTP API 提供。
- 583 页扫描 PDF 已由用户独立完成验证，不再作为当前 backlog 事项。
- 上述完成仅指首个版本的受限发布资格预审；P2.4-R1 MVP、P2.4-R5 周边编排和 P2.4-R3 报告生命周期已关闭。不得将当前 Agent 预审描述为完整合规审查。
- Elasticsearch 只读锁恢复、文档管理优化、OCR 分页恢复和相关反馈项已在历史验收中关闭；若运行环境再次出现，应创建新的带证据条目。

## Acceptance gates

- 发布中心变更必须执行 [`docs/release-center-functional-acceptance.md`](release-center-functional-acceptance.md) 要求的快速矩阵和完整业务矩阵。
- 产品体验巡检或 Web 工作台交互变更必须执行 [`docs/product-experience-acceptance.md`](product-experience-acceptance.md)，问题记入 [`issues/findings-register.md`](../issues/findings-register.md)；不能只用隔离栈绿报代替页面结论。
- 生产晋级必须同时有 Go/Python/Web、Compose、安全、确定性评估以及相应的真实模型、负载和恢复证据。
- 所有开放事项必须补充责任人、外部依赖、验收命令和最后核验日期后才能进入实施。

## Archive policy

- 完成事项记录关闭日期和证据链接后留在 Git 历史中，不在当前 backlog 重复维护，也不再把大段历史快照放回工作树。
- 历史条目中的旧状态按其日期理解；当前状态以本文件和对应设计/验收文档为准。
- 新增事项使用唯一 ID；关闭事项记录关闭日期和证据链接，不删除 Git 历史。
