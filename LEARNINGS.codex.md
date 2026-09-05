# Codex Learnings

这是项目的精简、可追加学习日志。历史 PRAR 记录不删除，完整快照保存在
[`docs/archive/LEARNINGS.codex-2026-09-05.md`](docs/archive/LEARNINGS.codex-2026-09-05.md)。
后续条目只记录可复用的工程结论、边界和验证结果，不重复完整提交说明或临时运行日志。

## 2026-09-05 - 学习日志归档重构

- 将 2026-08-19 至 2026-09-05 的 109 条原始记录完整归档，保留历史审计上下文。
- 根文件改为当前可检索的长期结论和开放限制；新的 PRAR 记录继续按日期追加。
- 当前发布中心、生成代际、身份会话和租户隔离的详细演进仍以归档快照和对应设计文档为准。

## Durable engineering lessons

- PostgreSQL 是文档版本、generation、发布 release 和审批决策的权威；搜索索引、Agent Run 和缓存都不能替代它。
- 发布审批必须绑定 immutable exact candidate（version、generation、digest、revision），批准前重新校验，候选变化时 fail closed。
- Agent 输出只是版本绑定的非可信审核证据；权限、审批人数和发布状态只能由确定性策略与人工审批改变。
- 租户、知识空间、生命周期和证据过滤必须在检索前确定；未知身份、跨租户访问、草稿、退役版本和依赖故障均应 fail closed。
- 异步 ingestion 使用 PostgreSQL admission/outbox、租约、幂等终态和 generation manifest；仅有搜索索引计数不足以证明完成。
- 评估必须区分 mock、公开基准、技术候选和业务 Gold；安全负例需要独立的零容忍门禁。
- Web BFF、HTTP handler、脚本和真实依赖验收都是公开行为边界；单元测试或 happy path 不能替代业务矩阵。

## Open limitations at archive time

- 企业身份生产启用仍等待 IdP、连接器负责人、权限/会话/MFA、保留期和 staging 验收等外部决策。
- P2.3 retention cleanup 默认关闭；启用需要获批的 `INDEX_RETENTION_WINDOW` 和生产部署证据。
- 私有业务 Gold 仍需要授权责任人的签名批准，不能用技术候选替代。
- 扫描 PDF 的 583 页真实文件尚未完成一次完整生产数据验收。
- 内容级预审目前是确定性首个切片，不代表完整的语义合规或敏感信息分类能力。
- 预审报告的 `expires_at` 字段和过期校验已存在，但当前协调器不设置 TTL，也没有过期清理或自动重审调度。
- 协调器曾接受 Agent `status=success`，但 PostgreSQL 只允许 `completed/failed/expired`；现已将 `success`（含大小写变体）规范化为 `completed`，仍需在真实发布中心矩阵中保留 PG 验收证据。
- Agent 编排的完整 Saga、异步审批通知和外部工作流引擎仍属于 MVP 范围外。

## 2026-09-05 - Agent review status contract

- Perceive: the coordinator accepted `success`, while the durable PostgreSQL review schema only allowed `completed`, `failed`, and `expired`.
- Reason: normalize provider aliases at the coordinator boundary so persisted reports always satisfy the durable schema without weakening fail-closed validation.
- Act: added a regression test and canonicalized trimmed, case-insensitive `success` to `completed` before saving the review report.
- Refine: the focused release-center coordinator tests pass; the full PostgreSQL-backed release-center matrix remains the deployment evidence gate.

## 维护约定

- 新条目按 `## YYYY-MM-DD - 主题` 追加，包含 Perceive、Reason、Act、Refine 四个要点。
- 只记录持久化教训、风险边界和可复核的验证结果；运行 ID、临时数量和环境细节应放在验收报告或提交记录中。
- 不改写历史条目；需要纠正时追加一条带日期的更正记录。
