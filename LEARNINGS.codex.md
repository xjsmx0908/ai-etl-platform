# Codex Learnings

这是项目的精简、可追加学习日志。后续条目只记录可复用的工程结论、边界和验证结果，不重复完整提交说明或临时运行日志。
2026-08-19 至 2026-09-05 的完整 PRAR 快照已移出工作树，需要时从 Git 历史查阅。

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
- 预审报告生命周期已由 P2.4-R3 补齐：创建时写入 TTL，到期后标记 expired 并自动重审同一候选；已发布/已拒绝证据不改写。
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

## 2026-09-07 - 预审复用 RAG 模型配置

- Perceive：语义预审已有 OpenAI-compatible 适配器，但独立 endpoint/model/key 为空时未启用，和 RAG Query 的现有模型配置重复。
- Reason：预审默认应复用 `LLM_ENDPOINT`、`LLM_MODEL` 和 `LLM_API_KEY`/`LLM_API_KEY_FILE`，独立变量只作为专用模型覆盖。
- Act：更新 API 初始化回退逻辑、示例配置和技术设计，并增加默认复用、专用覆盖及密钥文件回归测试。
- Refine：通过容器内 Go 相关测试与 Python 验收契约测试；完整 Compose 矩阵待发布中心变更验收时执行。

## 2026-09-07 - 自主预审 Agent 重新设计

- Perceive：现有 `ReviewPlanner` 固定调用资格工具后结束，semantic reviewer 只是固定流程中的单次模型判断，不满足自主预审目标。
- Reason：应保留现有编排器、确定性门禁和审批底座，在新的 `ReviewAgent` seam 后集中实现动态规划、只读工具治理、证据账本、报告校验和恢复。
- Act：新增 Agent 预审架构与实施方案，定义状态机、工具 registry、证据链、权限和预算边界、持久化、shadow/canary 迁移以及 R1A～R1E 验收计划。
- Refine：backlog 主线已改为先确认架构与接口，再实施自主 Agent；现有 semantic reviewer 仅作为可复用 adapter，不再代表 R1 已接近完成。

## 2026-09-07 - 自主预审方案收缩

- Perceive：首版方案同时引入新 runtime、工具守卫、证据账本、五类持久化对象和 shadow/canary，超过最小自主闭环需要。
- Reason：真正的 Agent 最小条件是模型能够基于 observation 选择下一工具或结束；企业安全可直接复用现有 Orchestrator、Registry、Authorizer、Run/Step、确定性门禁和审批。
- Act：方案收缩为 Review Planner、四个只读工具、最小 evidence refs 校验和发布中心接入，不新增第二套状态机或证据表。
- Refine：版本对比、政策库、独立证据生命周期、shadow/canary 自动化和 Saga 全部后置，MVP 以完整发布矩阵和真实模型场景作为完成门禁。

## 2026-09-07 - 最小自主预审闭环实施

- Perceive：旧发布 handler 仍依赖固定内容扫描和单次 semantic reviewer，上一轮执行在测试迁移前中断且未及时反馈。
- Reason：直接复用现有 Orchestrator、Run/Step、Registry、Authorizer 和发布中心门禁；Review Agent 仅允许四个只读工具，并由服务端校验 exact candidate、证据和确定性 findings。
- Act：实现 Review Planner/LLM observation 循环、四个工具、报告校验、发布中心接入；预审统一复用 `LLM_ENDPOINT`、`LLM_API_KEY` 和 `LLM_MODEL`，移除独立 semantic-review 配置及旧适配器。
- Refine：Go 全模块、175 项 Python 契约、快速发布中心矩阵和隔离栈 10 场景业务矩阵通过；真实模型场景仍是 P2.4-R1 关闭条件。

## 2026-09-07 - 自主预审进展文档同步

- Perceive：README、PRD、SRS、架构/技术设计仍混有固定 semantic reviewer 和“尚未实现”的描述。
- Reason：对外首页、产品目标、技术事实和执行状态必须共享同一口径，并明确区分代码完成、确定性验收通过和真实模型验收未完成。
- Act：更新平台架构、Review Agent 工具/模型配置、安全边界、验收命令和 P2.4-R1 状态；不把有限扫描描述为完整合规能力。
- Refine：后续以 `docs/backlog.md` 为状态源，以 Agent 预审实施方案为范围源，README 只保留可运行入口和最新摘要。

## 2026-09-07 - Review Run 累计 token 预算

- Perceive：预审已有单次 Planner `max_tokens`、步骤数和运行超时，但没有整次 Run 的累计 token 约束与审计字段。
- Reason：预算必须由通用 Orchestrator 在每轮 Planner 返回后累计，持久化到 Run/Step，并在超限时 fail-closed；模型未返回 usage 时使用保守估算，避免绕过预算。
- Act：新增 `AGENT_REVIEW_MAX_TOKEN_BUDGET` 配置、Run/Step token 用量、Planner usage 解析与重试累计、超限失败路径、Web 审计展示及回归测试。
- Refine：Go 全模块、`go vet`、175 项 Python 契约、Web lint/build 和发布中心隔离栈 10 场景矩阵通过；真实模型预算消耗和恢复专项证据仍属于 P2.4-R1 验收项。

## 2026-09-07 - Review Agent 恢复与 exact candidate 绑定

- Perceive：通用 Orchestrator 已能恢复 waiting tool，但 Review Agent 入口每次重试都会新建随机 Run，且恢复时重新 Assess 可能切换到新候选。
- Reason：预审审计链必须按 tenant + exact candidate 使用稳定 Run ID；Run Memory 保存候选快照，恢复时候选漂移立即 fail-closed。
- Act：新增 `StartOrResume`、稳定 review Run ID、`ResumePublicationReport`，并在 Review 工具加载时强制校验持久化候选；补充恢复不重复步骤与候选漂移测试。
- Refine：`internal/agent`、`internal/agentapi` 聚焦测试和临时 Redis 跨实例重启测试通过；发布隔离栈的 Redis/Compose 故障注入仍需作为部署专项证据保留。

## 2026-09-08 - 真实模型累计 token 预算验收

- Perceive：代码已有 Run 级累计 token 预算和失败转人工，但缺少通过生产 HTTP 入口的真实模型终止证据。
- Reason：验收必须同时观察发布请求、只读 review report 和 Agent Run，并核对每个 Planner step 的 usage 与 Run 累计值；报告不得保存提示词、文档内容或凭据。
- Act：新增真实模型专项脚本和脱敏报告；发现模型在超预算同时返回非法决策时错误原因会覆盖预算原因，调整 Orchestrator 使 `token_budget_exceeded` 优先并加入回归测试。
- Refine：本机 Ollama `qwen2.5:1.5b` 真实 Compose 验收通过：预算 64、累计 706，Run 为 `failed/token_budget_exceeded`，发布请求为 `manual_exception`，报告建议 `manual_review`。

## 维护约定

- 新条目按 `## YYYY-MM-DD - 主题` 追加，包含 Perceive、Reason、Act、Refine 四个要点。
- 只记录持久化教训、风险边界和可复核的验证结果；运行 ID、临时数量和环境细节应放在验收报告或提交记录中。
- 不改写历史条目；需要纠正时追加一条带日期的更正记录。

## 2026-09-05 - Codex 回答与上下文开销规则

- Perceive：项目规则同时包含中英文自然语言，且缺少对上下文和输出长度的统一约束。
- Reason：技术标识符保留英文有利于准确执行，但自然语言统一为中文可减少歧义和重复解释。
- Act：统一 `AGENTS.md` 的自然语言规则，明确先给结论、限制要点数量、不输出内部思考过程和完整日志，并要求限定读取范围。
- Refine：同步 `docs/backlog.md` 维护记录；后续任务继续使用 `CONTINUATION.md` 进行阶段续接。

## 2026-09-08 - Review Agent 部署恢复验收

- Perceive：恢复代码和临时 Redis 单测已有，但缺少隔离栈中 query-api 崩溃与 redis-state 往返后的部署证据。
- Reason：必须先持久化至少一个只读工具步骤，再通过 Compose 强制中断接口并恢复 Redis；恢复后只能继续同一 Run，且不得重复执行已完成工具。预审替身需要监听 `0.0.0.0`，否则容器无法连到宿主机回环地址。
- Act：新增可暂停 Planner 替身、隔离栈恢复脚本和脱敏报告；故障序列为 `kill query-api` → 停止/启动 `redis-state` → 再启动 query-api。
- Refine：隔离栈验收通过，同一 `review-run` 在故障前后保持，首个工具幂等键不变，四个只读工具加最终报告共 5 步，请求进入 `approval_pending`。

## 2026-09-08 - 真实模型四场景验收

- Perceive：P2.4-R1 剩余关闭条件是用真实模型验证普通、敏感信息、提示词注入和证据不足四类文档，并记录延迟、token 和人工转交。
- Reason：真实小模型会提前交卷、漏工具、省略确定性 finding，或在无证据时给出 needs_info。服务端必须在必做工具完成前拦截 final，补齐确定性 finding/风险下限，并禁止无证据阻断发布。
- Act：新增隔离栈验收脚本；Planner 兼容 markdown/`<think>`/对象 final/无 type 决策；Review 增加占位稿 `insufficient_evidence` 扫描；提前结束会被重定向到缺失的只读工具。
- Refine：本机 Ollama `qwen2.5:1.5b` 四场景通过。普通文档 `approval_pending/publish/low`；敏感信息和提示词注入 `needs_info/high` 且 finding 绑定 chunk；占位稿 `needs_info/medium`。证据保存在 `artifacts/release-center-real-model-scenarios-acceptance/`。P2.4-R1 MVP 关闭。

## 2026-09-08 - 续接文档口径对齐

- Perceive：R1 已关闭，但 CONTINUATION.md 仍禁止将 R1 标记为 completed。
- Reason：续接文档必须与 backlog/README 同一口径，同时保留“不是完整合规审查”边界。
- Act：删除过期限制，改为 R1 MVP 已完成，并更新当前分支/关闭提交说明。
- Refine：仅文档口径修复，不改变代码或验收门禁。

## 2026-09-08 - P2.4-R5 通知 outbox 与反向补偿

- Perceive：R1/R2 已能产生待审批和人工例外，但管理员只能轮询页面；Orchestrator 只补偿当前失败工具，不能回滚已经成功的副作用步骤。
- Reason：通知必须与业务状态分开、内容脱敏、幂等且 fail-open。Saga 只补偿注册了 compensator 的副作用工具，只读预审和幂等发布不自动撤销。
- Act：新增 `governance_notification_outbox` 与 `internal/notification`，在打开请求、记录决定、拒绝/发布和候选失效时入队；Query API relay 投递到 `/notifications`。Orchestrator 在失败/取消/超时后反向补偿已完成副作用步骤。
- Refine：通知故障不得改变审批或发布结果；未配置企业通道时跳过外发。外部 BPM 引擎仍不内嵌，只复用 webhook 与现有 `Decide` API。

## 2026-09-08 - P2.4-R5 外部工作流适配与真实通道通知

- Perceive：R5 已有 outbox 和补偿，但企业微信/钉钉消息不可读，外部审批引擎也没有稳定回写入口。
- Reason：不内嵌 BPM。出站继续用同一内容安全事件；入站用服务令牌解析在职管理员后复用 `Decide`，不得绕过审批组、自审和精确候选。
- Act：通知增加发布中心深链和 `decision_path`；alert-webhook 投递中文企业通道并可选转发工作流 webhook；新增 `POST /v1/release-center/workflow/decision`。
- Refine：通道故障不得改变审批结果。未配置企业通道或 callback token 时跳过/关闭适配器。

## 2026-09-08 - P2.4-R3 预审报告过期与自动重审

- Perceive：报告已有 `expires_at` 和过期拒绝，但创建时不写 TTL，采集器也不会过期或重审同一候选。
- Reason：过期只能影响未完成审批。已发布/已拒绝要保留当时证据。同一候选重审必须换新报告 ID、作废旧票，且不能把 Agent 的 `needs_info` 当成过期重审。
- Act：默认 TTL 7 天、保留 90 天；到期后请求进入 `needs_info` 并自动重审；幂等新 ID；无引用的过期报告才清理。
- Refine：过期不得绕过审批人数、自审限制和精确候选。`RELEASE_REVIEW_TTL=0` 可关闭过期，现有单测默认不启用 TTL。

## 2026-09-08 - 知识空间适配预审

- Perceive: 剩余“完整语义/隐私/合规审查”过大；用户要的是这份材料适不适合进这个知识空间、能不能当正式知识用，而不是材料类型分类器。
- Reason: 对齐企业知识管理：空间用途由管理员写，预审只判断适配和可用性，类型只作备注；版本对比、政策库和冲突检查暂缓。
- Act: 知识空间增加 purpose；新增 `assess_knowledge_fitness` 只读观察和服务端发布下限；结果只写在预审报告上。
- Refine: 无用途时不启用空间适配门禁，以免打断现有受管发布路径；有用途则 match+usable 才能建议发布。

## 2026-09-08 - P2.4-R6-ACCEPT 真实环境验收

- Perceive：R6 代码已有空间用途和适合性工具，但还缺隔离栈/真实模型证明：无用途可发布、匹配可发布、放错空间不可发布、聊天记录不能当正式知识，且类型备注不得写入文档。
- Reason：隔离栈用规则 Planner 覆盖 13 场景；真实小模型会把聊天记录误判为 `publish/usable`，所以适合性评估必须读取原文，并对非正式材料设置服务端下限。
- Act：功能矩阵扩到 13 场景，真实模型扩到 7 场景并要求第五个工具；`EvaluateKnowledgeFitness` 对聊天/即时通讯内容强制 `not_knowledge`。
- Refine：隔离栈 13/13 通过。真实模型前 6 场景通过后，非正式材料补跑也通过：`needs_info/reject`、`knowledge_usable=not_knowledge`、五个只读工具齐、类型备注未写入文档。证据保存在 `artifacts/release-center-functional-acceptance/` 与 `artifacts/release-center-real-model-scenarios-acceptance/`。

## 2026-09-09 - 产品体验验收文档落地

- Perceive：仓库已有发布中心/治理/身份专项验收，但真实 Web 的美观、使用逻辑和缺陷没有单一登记处；`issues/bugs.md` 九条口头问题也未复验关闭。
- Reason：体验巡检必须与隔离栈矩阵分开，否则会把脚本绿报当成页面可用；问题要分级、可复现、带证据，验收中途不改代码。
- Act：新增 `docs/product-experience-acceptance.md` 和 `issues/findings-register.md`，将旧口头问题迁移为 UAT-001～UAT-009 待复验；同步 backlog/README/SRS/PRD。
- Refine：下一步从 D0/D1 开始用真实页面复验旧九条，不以代码里已有 SSE/分页实现直接结案。


## 2026-09-09 - 清理过时上下文材料

- Perceive：根目录旧 `LEARNINGS.md`、`docs/archive/` 大快照、误提交 PDF/会话文件和过时模块 README 会在每次 Codex 检索时占用大量 token，并可能让模型学习到早期架构。
- Reason：当前状态已由 `LEARNINGS.codex.md`、`docs/backlog.md` 和现行设计/验收文档覆盖；历史快照保留在 Git 历史即可。
- Act：删除过时归档、无效二进制/会话文件和独立 Parser Compose；将 `services/etl-worker/README.md` 收成现行模块说明；收紧 `.codexignore`。
- Refine：不删除现行设计文档、验收脚本、评测 gold、用户语料 `rag_datas/` 和 `issues/`。


## 2026-09-09 - 删除无后续用途的历史材料

- Perceive：上次只删了过时归档。评测阶段报告、一次性压测快照、口头 bugs 副本和根 README 的长文仍会在检索时占上下文，但其中多数对后续实现没有新信息。
- Reason：CI gold、验收脚本、P2.5 身份设计、P1.9 Gold 管线必须保留。历史实验结论已在 ADR 和默认配置里。不能把“不进 Codex 上下文”的运行材料直接删掉。
- Act：删除 p1.3–p1.7 实验报告、压测快照、口头 bugs 副本和企业级愿景重复稿；README / evals README 收成现行入口；`.ignore` 让 rg 跳过续接备忘和合成评测集。
- Refine：身份设计、验收矩阵、golden-set、治理脚本一律保留。

## 2026-09-09 - 产品体验验收 D4 回写

- Perceive：真实 Web 巡检证据已在 `artifacts/product-experience-acceptance/2026-09-09/`，但矩阵/登记册/backlog 仍停在 11:20 模板，D4 未写。
- Reason：验收中途不改产品代码；结论必须来自页面证据。旧九条能关则关，失败项进登记册，S1 才回写 backlog。本轮不能把 P-UAT-1 标完成。
- Act：填写 PX-01～11；UAT-001～006/008/009 已修复，UAT-007 仍开放；新增 UAT-010～013。P-UAT-1 改为 in-progress / 本轮未通过，并单列只读问答 S1。
- Refine：机密双审和删除/退役问答是覆盖缺口不是已证实缺陷。证据目录 Git 忽略，不入库账号密码。

## 2026-09-09 - Resume must not re-diagnose

- Perceive: 续跑时把「Verify, Then Trust」做成整包重读 findings/ES/源码，120k goal 在诊断阶段耗尽，代码未动。
- Reason: 冻结诊断写进 task-status 后，续跑只开 Next files；核实针对补丁结果，不针对已知根因。
- Act: 更新 ~/.codex/AGENTS.md 1.1 与 task-status 字段：Frozen diagnosis / Do not repeat / Next files。

## 2026-09-09 - 修复 UAT-010 并补 PX-06/PX-09

- Perceive：只读能看见已发布「赴港流程」，但问答拒答。标题在 `documents.file_name`，chunk 正文不含标题；该文档 release 为 unresolved 且无 generation。
- Reason：问句匹配已发布文件名后用 ES `doc_id` 召回；已发布但无 generation 的 chunk 允许作为证据。问答列出全部授权空间，0 个时给空状态。质量页空状态写明离线评测。
- Act：改 query/retrieval/visibility 与 QA/quality 页；`make` 相关 Go 测试与源码契约通过。真实页面：只读命中赴港流程；机密双审 2/2 已发布；质量页空状态说明离线评测。
- Refine：SSE 不结束（UAT-011）、质量报告未挂载（UAT-007）、只读上传按钮（UAT-012）、登录英文错误（UAT-013）仍开放。现网修复后需 flush 语义缓存。

## 2026-09-09 - 续跑入口写入文档

- Perceive：UAT-010/PX-06/PX-09 已验证并推送 `bf5832d`，但续跑入口只在 `~/.codex/task-status.md`，仓库内仍容易重跑 D0–D4。
- Reason：剩余项是 UAT-011/012/007/013；冻结诊断和 Next files 必须同时写进 backlog 与登记册。
- Act：更新 task-status、fix 轮 notes/matrix、backlog `P-UAT-1-next`、findings 续跑段。
- Refine：下一会话直接改 `querySSE` 结束条件，不要重读 ES。

## 2026-09-09 - 修复 UAT-011 问答 SSE 不结束

- Perceive：服务端已写 `event: done`，但 `querySSE` 等到 HTTP body EOF；问答页只在 Promise resolve 后离开 streaming，巡检空等约 70s。
- Reason：应用层 `done`/`error` 就是流结束；客户端必须立即 return 并 cancel reader。done 态还要画出「回答已完成」，否则巡检看不到完成标记。
- Act：抽出 `consumeQuerySSEStream`，终端事件后结束；问答页 onDone 置 done 并展示完成标记。契约测试覆盖 hanging stream。
- Refine：`px-user` 巡检问答 3642ms 出现「回答已完成」。下一步 UAT-012，不重跑 D0–D4 / UAT-010 / UAT-011。

## 2026-09-09 - 修复 UAT-012 只读上传按钮

- Perceive：只读打开 `/data` 后可选文件，上传按钮 `disabled=false`，提交才 `forbidden`。
- Reason：前端必须按角色禁用，并说明无权限；不能只靠后端拒绝。
- Act：抽出 `canUploadDocuments`；只读禁用文件/空间/密级/上传，展示「没有数据接入权限」。
- Refine：`px-readonly` 按钮与文件选择均 disabled，无 forbidden；`px-user` 选文件后仍可上传。下一步 UAT-007，不重跑 D0–D4 / UAT-010 / UAT-011 / UAT-012。

## 2026-09-09 - 修复 UAT-007 检索质量报告未挂载

- Perceive：PX-09 空状态已过。`docs/evals/reports/` 为空，compose 把该目录挂到 `/app/public/evals`，盖掉烘焙 `latest.json`，`/evals/latest.json` 404。
- Reason：禁止空 reports 覆盖 public/evals；有 `latest.json` 再拷贝。real eval 应写出质量页 schema 的 `latest.json`，mock/invalid 不覆盖。
- Act：compose 改挂 `/eval-reports` 并 copy-if-present；`write_report()` 写 latest.json；契约测试覆盖。
- Refine：`px-user` 打开 `/quality` 见 Recall@1 55% 与离线评测说明。下一步 UAT-013，不重跑 D0–D4 / UAT-007 / UAT-010 / UAT-011 / UAT-012。

## 2026-09-09 - 修复 UAT-013 登录失败英文提示

- Perceive：错密登录页直接展示 API `invalid credentials`。BFF 已有中文 fallback，但优先透传 upstream `error`。
- Reason：登录页与 BFF 共用映射，避免英文 API 文案漏到中文界面。
- Act：新增 `localizeLoginError`；401 映射为「用户名或密码错误」，403 映射为「账号已停用」。
- Refine：API 401 与页面红字均为「用户名或密码错误」，无英文原文。P-UAT-1 关闭。

## 2026-09-09 - 拆分 Query API upload/health 入口

- Perceive：上下文膨胀主要来自少数 god file，不是仓库整体过大。`cmd/api/main.go` 约 1746 行，上传、健康检查与组装混在一起。
- Reason：第一刀只做同包机械拆分，不改 HTTP/权限语义，不抽新接口。
- Act：抽出 `health_handlers.go`、`upload_handlers.go`、`task_handlers.go`；`main.go` 只保留组装与后台 relay。
- Refine：`go test ./cmd/api -count=1`、`go vet ./cmd/api`、`go build ./cmd/api` 通过。下一步如继续精简，再拆 `query/service.go`，不重做本次拆分。

## 2026-09-09 - 体验验收覆盖缺口补测

- Perceive：正式轮已通过，但退役问答、受管换版和新建用户未跑完。续跑脚本曾把列表里其他文档的「需补齐」当成结束条件。
- Reason：验收中途不改产品代码。个人空间与用户管理可以当场补测；受管换版必须等预审/审批，不能用占位短文绕过空间适配门禁。
- Act：真实页面补测个人换版/退役/新建用户均通过。受管路径上，换版过程中旧发布仍可问；新版本发布后同一文档并列旧/新切块，问答仍引用 80 元而非 120 元，登记 UAT-014。
- Refine：不要根据整页「需补齐/已发布」判断当前文档状态；发布态以详情页徽章和批准按钮为准。合成短文会被生产库用途门禁判为非知识。

## 2026-09-09 - 修复 UAT-014 受管换版未切换

- Perceive：已发布受管文档换版后 catalog 仍为 published，`ListReviewJobs` 只要 draft，发布中心把整页当成已发布，v2 无法预审。检索仍指向旧 generation。
- Reason：换版期间必须保持旧发布可问，所以不能在上传时删切块。缺口是让 current≠published 的文档进入预审/审批，发布时退役其他 generation，详情按已发布 generation 过滤。
- Act：`ProjectOverview` / `ListReviewJobs` 覆盖 published replacement；发布退役旧 generation；详情 `ResolveVisibility`。发起人不能自审，需第二管理员批准。
- Refine：`px-admin-2` 批准后问答 120 元，详情不再列出 v1 标记。不重跑 D0–D4。

## 2026-09-09 - 查询负载脚本改为性能门禁

- Perceive：`scripts/load-test.py` 已能并发打 `/v1/query` 并输出 p95，但始终 exit 0，不能当回归门禁。
- Reason：先做工程预算门禁，不发明 ADR 0010 生产 SLO。冷检索与缓存/生成路径分开，默认 `user-uploads`，不把发布审批编进压测。
- Act：增加 `--profile`、阈值、`--retrieval-only`、登录和 `evaluate_gate()`；越线 exit 1。补 `scripts/tests/test_load_test.py`。
- Refine：契约测试覆盖百分位、profile、报告和本地假 API 的 pass/fail。真实隔离栈校准与 nightly workflow 仍未做。

## 2026-09-09 - 查询门禁改为隔离 nightly

- Perceive：演示栈是真实 bge-m3/LLM，diagnostics 关闭，内存不足以再起第二套 Compose。不能用它校准 mock 工程预算。
- Reason：L1 门禁必须走独立 mock 栈；演示栈只做脚本连通，避免 40 次真实生成。
- Act：新增 `scripts/load-test-gate.sh` 和 `workflow_dispatch`/nightly workflow，不加入 Required Checks。
- Refine：本机用 `tenant-loadtest` JWT 做小流量连通。阈值仍待隔离栈三次校准。

## 2026-09-10 - 入库容量轨道与 L1 查询门禁分离

- Perceive：L1 mock 查询门禁已通过；用户要看真实模型和入库性能。演示栈是 CPU bge-m3 + 远程 LLM，不能再起第二套 Compose，也不能用 40 并发真实生成校准 L1。
- Reason：质量、入库容量、真实问答时延必须分轨。先做可在演示栈串行运行的 accepted-to-ready 测量；真实 RAG 评测等待停栈窗口。
- Act：新增 `scripts/ingestion-capacity.py`、契约测试和 `docs/ingestion-capacity.md`。脚本不调用 `/v1/query`、不起 Compose。
- Refine：契约测试 6/6 通过。演示栈 CPU bge-m3 串行：short-text 2 chunk / 11.0s，typical-doc 12 chunk / 24.0s。短文本不等于扫描 PDF。P-CAP-2 仍需停演示栈才能跑 `--real-models`。

## 2026-09-10 - 补全入库阶段耗时记录与展示

- Perceive：演示栈能看到整单入库墙钟，但任务/文档没有 parse/OCR/embed/store 毫秒。缺的是字段和展示，不是另开评测栈。
- Reason：在当前 Compose 记阶段耗时：任务 Redis JSON、documents.stage_timings、Prometheus histogram、上传页和文档页。
- Act：pipeline 计时 + observer；migration 0029；API `stage_timings`；Grafana Parse/Embed/Store/OCR p95。完成态保留 chunks_done。
- Refine：Go 测试通过。演示栈短文本 `doc-1789008297108283450`：parse 4ms / embed 7868ms / store 314ms / total 8231ms。旧文档仍显示 —。

## 2026-09-10 - 问答 SSE 真流式

- Perceive：工作台虽发 `Accept: text/event-stream`，但 `/v1/query` 等 generate + grounding 全部结束后才吐一个 delta，首字等于整段完成。
- Reason：检索通过后即可推 token；JSON 路径保持整段返回。grounding 仍在 done 前执行，失败用 `replace` 覆盖，避免未校验答案成为终态。
- Act：`ask` 增加 `queryStream`；流式走 `streamChat`；`done.answer` 为最终答案。Web 用 `replace`/`done.answer` 覆盖。
- Refine：`go test ./internal/query` 与 `scripts.tests.test_query_sse_client` 通过。入库仍受 CPU embedding 限制，本切片不改。

## 2026-09-10 - 钉住本地 embedding 去掉空闲冷启动

- Perceive：用户觉得问答十几秒、上传很久不符合企业接口。`/v1/upload` 已是 202；短文本 embed 7.9s 是 CPU bge-m3 冷加载。问答检索约 0.4s，总时长是远程 LLM。keep-alive 已写未部署。
- Reason：Ollama `keep_alive=24h` + 启动预热可以消掉空闲冷加载，不能把大文档 CPU 推理变成 GPU。不提高 `EMBED_CONCURRENCY`，不起第二套 Compose。query-api 预热放后台，避免 HEALTHCHECK start-period 5s 被堵。
- Act：embedder/retrieval 发送 keep_alive；worker 同步预热，query-api 异步预热；问答日志补 retrieval_ms/ttft；工作台展示首字耗时。重建 etl-worker、query-api、web。
- Refine：短文本 embed 487ms、就绪 2.0s、上传 HTTP 28ms。问答进度 2ms，首字 3.1–4.9s。大 PDF 仍受 CPU embedding 限制。

## 2026-09-10 - 发布中心红色 Fail to fetch

- Perceive：知识发布中心时不时出现红色 `Fail to fetch`。页面每 8 秒并行拉 documents/requests/overview；`catch` 直接 `setError(e.message)`，成功刷新不清空。query-api overview 近 4h 202 次全 200。
- Reason：红字是浏览器/BFF 瞬时 `fetch` 失败被放大，不是审批业务 5xx。数据接入页已对同类代理抖动做过容忍，发布中心没有。
- Act：静默轮询忽略瞬时网络错误；成功刷新清横幅；BFF `proxyBackend` 捕获上游 fetch 失败返回 503 JSON。
- Refine：`python3 scripts/tests/test_release_center_fetch_error.py` 通过。UAT-015 待真实页面复验。

## 2026-09-10 - Top-K 截断不再挤掉第二篇必查文档

- Perceive：`/quality` 55/68/71 不是企业验收。gold-candidate-v2 跨文档 fused all-required 40% → selected 0%，最终 Top-5 先收下同一文档第 2 块。
- Reason：多样性应先覆盖不同文档，再补 maxPerDoc 以内的第二块；单文档结果仍允许填满剩余槽位。
- Act：`diversifyCandidates` 改为 unique-document-first；回归测试锁住 fused 两篇必查文档在 Top-5 仍都在。
- Refine：`go test ./internal/retrieval -count=1` 通过。未重跑真实模型评测，未停演示栈。

## 2026-09-10 - ES 中文 BM25 不再要求整句 AND

- Perceive：mapping 已是 `cjk` v2，再装 IK 解决不了 P1.8 的 ES 0%。`match operator=and` 等于问句每个 CJK bigram 都要出现；跨文档 gold 里多道第二篇与问句 0 重叠。
- Reason：保留 phrase 高精度，把 fallback 改成 OR + `minimum_should_match=50%`。不改镜像、不重建索引、不停演示栈。
- Act：`titleAwareShouldClauses` 的 content match 改为 OR/50%；更新检索单测。
- Refine：`go test ./internal/retrieval ./internal/es -count=1` 通过。这不能让无关第二篇变成必中。

## 2026-09-10 - 表格切块保留表头

- Perceive：大表按字符切断后，后面的行丢掉「工号/姓名」表头；噪声过滤器还可能把稀疏花名册当空表丢掉。
- Reason：Sheet 标记当作分界；超大 sheet 按行切并在每块重复表头；带 tab 的表格不走密度丢弃。
- Act：`chunker.py` 增加 tabular split；测试覆盖花名册、多 sheet、超大切块重复表头。
- Refine：parser `tests/test_chunker.py tests/test_office_parsers.py` 17 passed。未停演示栈、未重跑真实评测。

## 2026-09-10 - 检索质量页标明非企业 Gold

- Perceive：用户把 Recall@1/3/5 当成企业级标准。页面只说离线回归，没展示 note，也没写不是签字 Gold。
- Reason：在 `/quality` 写清「不是即时评分、不是企业 Gold、不能当 SLO」，并渲染 Recall@k 的 note。
- Act：更新 `quality/page.tsx`；增加 `scripts/tests/test_quality_page_copy.py`。
- Refine：文案单测通过。未停演示栈、未重跑真实评测。

## 2026-09-10 - 演示栈真实模型企业候选评测

- Perceive：用户不要 P1.9 签字，只要在现有演示栈上测系统能不能达到企业技术门槛；禁止第二套 Compose、禁止停站。
- Reason：`--api-base` 复用 Query API；企业候选 35 篇入库 user-uploads 后 retrieval-only 70 题。技术门槛沿用仓库 90% hit/Recall@5，不当作签字 Gold。
- Act：评测脚本跳过隔离 Compose，现有栈走宿主机 ES；入库 2 份大文件 embedding 超时后串行重传成功。报告 `docs/evals/private/p1.4-enterprise/live-demo-20260910/eval-20260910-205028.json`。
- Refine：Recall@1/3/5 = 50.9%/74.5%/78.2%，未达 90%。词面 100%、语义 93%、跨文档 selected all-required 0%、负样本 100%。未改 `/quality` 烘焙数字。

## 2026-09-10 - 跨文档 0% 是题集问题

- Perceive：10 道 cross-document 把无关第二篇标成 required，7 道直接复制「陈伟的OA账号是什么？」；fused rank 38 是正常排序，不该改 diversity 硬塞。
- Reason：跨文档题必须不能抄单文档问句，且问句与每个来源证据不能 0 重叠。有效题 60 道才对照 90% 门槛。
- Act：`validate_cross_document_candidate` 增加 `query_copied_from_single_document` / `query_unrelated_to_source`；过滤后在演示栈重跑 retrieval-only。
- Refine：有效集 Recall@1/3/5 = 62.2%/91.1%/95.6%，hit_rate 95.6%，负样本 100%。Recall@5 达到 90%；Recall@1 仍低。未改检索截断，未停演示栈。

## 2026-09-10 - 质量页改挂技术评测数字

- Perceive：用户把 `/quality` 的 55/68/71 当成企业标准，并问 P1.9 签字、停演示栈、第一名不稳。
- Reason：有效 60 题演示栈 Recall@5=95.6% 已过 90% 技术门槛；Recall@1=62% 是排序问题。抢第一名最多的是演示栈已有长文档（爱因斯坦传）和相似制度，不是题集过期，也不需要业务签字。
- Act：把 `/quality` 挂到 `eval-20260910-205904`（62/91/96），文案改成 Recall@5 才是技术门槛；P-CAP-2 记为可在演示栈 `--api-base` 完成。
- Refine：不启用英文 reranker，不停站。第一名要再往上走，需要中文 reranker 或压长文档，不指望 Recall@1 也到 90%。

## 2026-09-10 - 压零重叠长文档抬 Recall@1

- Perceive：第一名不稳主因是演示栈长文档（爱因斯坦传）弱匹配抢 unique-doc Top-1。
- Reason：按词面加分会把“通知/员工”类文档抬过头，Recall@5 从 95.6% 掉到 64%。只对“别人有重叠、自己接近 0”的候选降权。
- Act：`stabilizeRanking` 放在 exact pin 之前；缓存键 `retrieval:cache:v3`。单测覆盖短制度压长文档、精确命中仍钉住。
- Refine：演示栈有效 60 题 Recall@1/3/5 = 71.1%/91.1%/97.8%，负样本 100%。未停站，未开英文 reranker。

## 2026-09-10 - 中文 reranker 抬 Recall@1

- Perceive：用户问质量页要不要更新、第一名不稳能不能解决，并明确不要 P1.9 签字。
- Reason：技术门槛是 Recall@5/hit_rate ≥ 90%，不是 Recall@1。剩余 miss 是相似制度互抢，不是题集过期。演示栈 `--api-base` 可重考，不必停站。
- Act：现网打开 `BAAI/bge-reranker-base`，有效 60 题重跑 `eval-20260910-221343`。质量页改挂 80/100/100。
- Refine：Recall@1/3/5 = 80%/100%/100%，负样本 100%。剩余 9 题第一名仍被同类制度抢走。不停站、不走 P1.9。

## 2026-09-11 - rerank 带上文件名

- Perceive：剩余第一名 miss 多是近重复制度；rerank 只看 chunk 正文，ES 也没有 file_name 字段。
- Reason：给 cross-encoder 加标题，不给所有候选做词面加分。rerank 改为处理 hub 降权后的列表。
- Act：Query 把已发布文件名传入检索；HTTP reranker 前缀 `标题:`。有效 60 题重跑 `eval-20260911-122209`。
- Refine：Recall@1/3/5 = 82.2%/95.6%/100%，负样本 100%。OA账号已能靠文件名排到第一；Recall@3 从 100% 掉到 96%，低于噪声门槛不算回退。不停站、不走 P1.9。

## 2026-09-11 - 修复质量页 Recall@3 柱图错位

- Perceive：`/quality` 的 Recall@3 柱子比两侧低，96% 数字压在柱顶灰/蓝交界上。
- Reason：外层 `items-end` 按说明文字底对齐，Recall@3 的 note 只有一行，柱子被拉下去；百分比写在 `overflow-hidden` 柱内，96% 几乎顶满时会被裁切和叠色。
- Act：柱图改为顶对齐，百分比移到柱外，标签 `whitespace-nowrap`，说明区给 `min-h-8`。
- Refine：`python3 -m unittest scripts.tests.test_quality_page_copy scripts.tests.test_qa_quality_experience -q` 通过。未停演示栈。

## 2026-09-11 - UAT-015 发布中心红字复验

- Perceive：下一未完成项是 UAT-015 真实页面复验，代码已静默轮询瞬时 fetch。
- Reason：验收中途不改代码。用 px-admin 打开 `/release-center`，核对停留轮询、中断 overview、手动刷新三条路径。
- Act：Playwright 复验；静默失败无红字，手动刷新显示「同步暂时失败，请稍后重试」，恢复后横幅消失。
- Refine：登记册 UAT-015 与 backlog P-UAT-3 关闭。证据 `artifacts/product-experience-acceptance/2026-09-11-uat015/`。不停演示栈。

## 2026-09-11 - 产品壳层与页面语言

- Perceive：产品稿已给出统一壳层和中文页面语言；现网截图和工作树仍露出文档 ID、英文 stage 和技术路径。
- Reason：只改 Web 展示，不改发布策略、预审和 reranker。页头、导航分组、引用和阶段文案是同一批 S3。
- Act：落地 PageHeader、管理分组、问答依据折叠、数据接入中文阶段、文档文件名/空间名、发布中心页头；补 `test_product_shell.py` 与 UAT-016。
- Refine：契约测试覆盖本批文案。文档详情页本轮不做。未停演示栈。

## 2026-09-11 - 文档详情页对齐产品壳层

- Perceive：列表和接入页已中文化，详情页仍用英文治理状态下拉、空间 raw id、标题不是 PageHeader。
- Reason：同一批 S3，不改发布/替换语义；spaceLabel 抽到 docDisplay 避免两页各写一份。
- Act：详情页 PageHeader、空间名、有效/已替代/归档；替换确认用文件名；补 test_product_shell。
- Refine：契约测试通过。随后重建 web 镜像才能在 :3100 看到。

## 2026-09-11 - 壳层上线后现网观感复验

- Perceive：用户问整站是否已经设计合理。代码已推送且 web 已重建，需要真页面而不是契约测试。
- Reason：验收中途不改代码。用 px-admin 截九页加文档详情，对照产品稿只记 S3。
- Act：证据 `artifacts/product-experience-acceptance/2026-09-11-ux-retest/`。UAT-016 现网通过。开放 UAT-017～020。
- Refine：壳层/中文主路径已合理。未做视觉重设计。未改发布策略。

## 2026-09-12 - 安全加固计划

- Perceive：用户确认当前栈挡得住一部分应用层乱调，但默认 Compose 数据面暴露、登录无限流、`/metrics` 匿名，外部玩家能打崩或绕到 ES/MinIO。要求写出详细解决方式和计划，不要求立刻改代码。
- Reason：先收网络再补应用。P-SEC-0 把端口绑回环；P-SEC-1 在 bcrypt 前限流并给 metrics 加 token；P-SEC-2 让 Redis/MinIO/Qdrant 密码真正生效；P-SEC-3 给问答/上传加并发帽；P-SEC-5 收紧已有 Nginx；P-SEC-4 只做输出侧敏感信息拒答，不承诺提示词注入根治。
- Act：新增 `docs/security-hardening-plan.md`，更新 `docs/security.md`、`docs/backlog.md`、`README.md`。未改 Compose 与服务代码。
- Refine：完成标准是五条可验证条件，不是“加了 WAF”这种口号。下一步等用户指定从 P-SEC-0 开始落地。
## 2026-09-12 - Security hardening P-SEC-0 to P-SEC-5

- Perceive: the default Compose stack published data-plane and Query API ports on every interface; login and `/metrics` were unauthenticated; Redis/MinIO/Qdrant used empty or default secrets.
- Reason: close the network first, then unauthenticated HTTP, then real data-plane passwords, then authenticated concurrency caps, then Nginx and output-side secret refusal.
- Act: bind host ports to `127.0.0.1` via `COMPOSE_BIND`; rate-limit login before bcrypt; require `METRICS_TOKEN` outside dev; enable Redis `requirepass`, MinIO/Qdrant secrets; cap query/upload/agent concurrency; reuse publish-time sensitive-data regexes on answers.
- Verify: `gofmt`, `go test` on affected packages, `docker compose config`, and `scripts/tests/test_security_hardening.py`. Residual risk: prompt injection is still heuristic; ES/Kafka stay plaintext on the Docker network.
