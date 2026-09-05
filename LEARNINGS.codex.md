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

以下条目是归档时的事实记录，不代表当前执行顺序；当前路线以 `docs/backlog.md` 和
`CONTINUATION.md` 的“Current execution plan”为准。

- 企业身份生产启用仍等待 IdP、连接器负责人、权限/会话/MFA、保留期和 staging 验收等外部决策。
- P2.3 retention cleanup 默认关闭；启用需要获批的 `INDEX_RETENTION_WINDOW` 和生产部署证据。
- 私有业务 Gold 仍需要授权责任人的签名批准，不能用技术候选替代。
- 扫描 PDF 的 583 页真实文件已由用户独立完成验证，不再作为当前执行事项。
- 内容级预审目前是确定性首个切片，不代表完整的语义合规或敏感信息分类能力。
- 预审报告的 `expires_at` 字段和过期校验已存在，但当前协调器不设置 TTL，也没有过期清理或自动重审调度。
- 协调器曾接受 Agent `status=success`，但 PostgreSQL 只允许 `completed/failed/expired`；现已将 `success`（含大小写变体）规范化为 `completed`，仍需在真实发布中心矩阵中保留 PG 验收证据。
- Agent 编排的完整 Saga、异步审批通知和外部工作流引擎仍属于 MVP 范围外。

## 2026-09-05 - Agent review status contract

- Perceive: the coordinator accepted `success`, while the durable PostgreSQL review schema only allowed `completed`, `failed`, and `expired`.
- Reason: normalize provider aliases at the coordinator boundary so persisted reports always satisfy the durable schema without weakening fail-closed validation.
- Act: added a regression test and canonicalized trimmed, case-insensitive `success` to `completed` before saving the review report.
- Refine: the focused release-center coordinator tests pass; the full PostgreSQL-backed release-center matrix remains the deployment evidence gate.

## 2026-09-05 - Agent audit scope and execution order

- Perceive: the user confirmed that OCR is independently verified and that enterprise Agent audit completeness is the primary goal.
- Reason: production-readiness decisions and unrelated pipeline validation must not compete with the Agent audit feature roadmap.
- Act: set the durable execution order to P2.4-R2 → P2.4-R1 → P2.4-R5, with P2.4-R3 deferred; marked P1.9, P2.3, P2.5, P2.6, and OCR as non-current unless explicitly reactivated.
- Refine: synchronized `docs/backlog.md`, `CONTINUATION.md`, and the release-center acceptance document so new sessions have one unambiguous route.

## 2026-09-05 - P2.4-R2 approval groups and configurable policies

- Perceive: release-center approval previously had only a fixed one/two-admin rule and no tenant-owned approver identity model.
- Reason: policy configuration must be deterministic, tenant-scoped, and unable to weaken confidential/high-risk safety floors; membership must be checked again at decision time.
- Act: added PostgreSQL and in-memory approval groups, member activation, space/permission/risk policy matching, priority resolution, requester self-approval control, request policy persistence, and authenticated admin management APIs. Added a composite tenant/user foreign key and fail-closed behavior when a group-constrained request lacks its policy store.
- Refine: focused Go tests, the 10-scenario release-center functional acceptance, and the governance acceptance matrix passed. Enterprise IdP group sync, delegation, timed escalation, revocation workflows, and a dedicated Web policy editor remain explicitly outside this slice.

## 2026-09-05 - P2.4-R1 semantic review contract (in progress)

- Perceive: the existing release-center reviewer only performed deterministic eligibility and bounded pattern scans, so it could not provide model-backed semantic, privacy, or compliance evidence.
- Reason: model output must remain untrusted and version-bound; the boundary therefore needs an explicit input contract, structured result schema, exact chunk evidence validation, and fail-closed transport/schema handling before any model can influence a release recommendation.
- Act: added a replaceable semantic reviewer interface and OpenAI-compatible client, wired it after exact-candidate deterministic checks, merged only risk escalations/non-publish recommendations, and exposed endpoint/model/prompt settings through environment configuration.
- Refine: added HTTP-client, input-boundary, evidence-reference, handler-merge, and model-failure tests. Model evaluation datasets, business taxonomy calibration, and deployment acceptance remain before marking R1 complete.

## 维护约定

- 新条目按 `## YYYY-MM-DD - 主题` 追加，包含 Perceive、Reason、Act、Refine 四个要点。
- 只记录持久化教训、风险边界和可复核的验证结果；运行 ID、临时数量和环境细节应放在验收报告或提交记录中。
- 不改写历史条目；需要纠正时追加一条带日期的更正记录。

## 2026-09-05 - Codex 回答与上下文开销规则

- Perceive：项目规则同时包含中英文自然语言，且缺少对上下文和输出长度的统一约束。
- Reason：技术标识符保留英文有利于准确执行，但自然语言统一为中文可减少歧义和重复解释。
- Act：统一 `AGENTS.md` 的自然语言规则，明确先给结论、限制要点数量、不输出内部思考过程和完整日志，并要求限定读取范围。
- Refine：同步 `docs/backlog.md` 维护记录；后续任务继续使用 `CONTINUATION.md` 进行阶段续接。
