# Agent 预审架构与实施方案

## 1. 文档定位

本文将 Knowledge Release Center 的 Agent 预审从“固定资格流程中的模型审查器”
重新定义为“受约束、可迭代、证据驱动的企业级预审 Agent”。本文是后续实现、
验收和发布决策的基线；在本文被确认前，不继续扩展现有 `ReviewPlanner` 的固定
流程能力。

本文只设计受管文档的发布前审查。Agent 不拥有审批、发布、权限修改、元数据修改
或跨租户访问能力。

## 2. 目标与完成定义

### 2.1 产品目标

对每个 exact candidate，Agent 能够：

1. 理解文档属性、租户政策和审查目标；
2. 制定一份可持久化的审查计划；
3. 在只读工具范围内自主选择检查顺序和补充证据路径；
4. 根据工具观察决定继续检查、交叉验证、转人工或完成审查；
5. 输出每条结论都绑定证据的结构化报告；
6. 在预算、权限和确定性安全门禁内运行，并可恢复、重放和审计。

### 2.2 非目标

- Agent 不自行批准、拒绝审批或发布文档。
- Agent 不改变文档权限、治理字段、知识空间、候选版本或索引状态。
- Agent 不直接访问任意互联网、数据库、文件系统或未注册工具。
- Agent 不把模型的“低风险”结论作为绕过确定性门禁的依据。
- 首个版本不实现多工具副作用 Saga、异步通知和外部工作流引擎；这些属于
  Agent 编排扩展阶段。

### 2.3 完成定义

本方案的 MVP 只有在以下条件全部满足后，才可称为“企业级 Agent 预审”：

- 同一审查任务至少支持两种不同的有效工具路径，而不是固定单工具顺序；
- Agent 可根据观察结果继续、复核或提前结束，且计划和决策可恢复；
- 每条 finding、结论和阻断都包含 exact candidate、工具调用和证据引用；
- 未知证据、工具越权、预算耗尽、模型错误和持久化故障均 fail-closed；
- Agent Run、工具调用、输入摘要、输出校验、证据链和最终报告可审计；
- deterministic eligibility、tenant/RBAC、exact-candidate revalidation 和审批
  仍由 Agent 外部的权威模块执行；
- shadow/canary 与完整业务验收矩阵通过后，才允许替换固定流程。

## 3. 当前实现诊断

当前实现的实际链路是：

```text
固定 ReviewPlanner
  -> assess_publication
  -> exact candidate/chunk 装配
  -> 固定规则扫描
  -> 单次 semantic reviewer
  -> 服务端合并风险与建议
  -> 固定审批策略
```

`ReviewPlanner` 只调用一次 `assess_publication` 后结束；语义模型只返回一次
结构化结果，不能选择后续工具、补充证据或根据冲突结果再次检查。现有
`internal/agent` 的状态机、工具注册、参数校验、RBAC、锁、恢复、超时和审计能力
是可复用底座，但发布预审目前仍是一个浅的固定流程模块。

## 4. 目标架构

### 4.1 模块与 seam

目标设计保留一个对发布中心足够小的深模块接口：

```go
type ReviewAgent interface {
    Review(context.Context, ReviewRequest) (ReviewReport, error)
}
```

`ReviewAgent` 是发布中心与自主审查实现之间的 seam。调用方只需提供版本绑定的
`ReviewRequest`，不需要知道 Planner、工具循环、证据账本或模型供应商的细节。
测试通过同一接口注入确定性 Agent adapter；生产实现隐藏多步编排和模型适配。

`ReviewRequest` 必须包含：

- tenant、actor 和 knowledge space 身份；
- document/version/generation/release revision/digest/count；
- 文档权限、治理字段和确定性 eligibility 结果；
- 审查目标、适用政策版本和预算；
- 允许的工具 capability 集合。

`ReviewReport` 必须包含：

- report/run/plan 标识和状态；
- exact candidate 完整绑定；
- recommendation、risk、summary 和需要人工处理的原因；
- findings、claims、evidence refs、工具调用摘要；
- model、prompt/policy version、预算消耗、开始/结束时间；
- `completed`、`needs_info`、`manual_review`、`failed` 等终态原因。

### 4.2 模块分层

```text
Release Center Coordinator
  -> ReviewAgent seam
       -> ReviewAgentRuntime
            -> ReviewPlanner adapter
            -> ToolGuard
            -> ReadOnly Review Tool Registry
            -> EvidenceLedger
            -> ReportValidator/Assembler
            -> Existing Agent Orchestrator state machine
  -> Deterministic eligibility / approval / publication modules
```

- `ReviewAgentRuntime` 是深模块：负责循环、预算、恢复、证据和报告完整性。
- `ReviewPlanner` 是可替换 adapter：可以是 OpenAI-compatible 模型、规则 planner
  或离线评测 planner，但不能直接获得工具或发布权限。
- `ToolGuard` 是不可绕过的内部 seam：执行前校验租户、candidate、scope、参数、
  read-only 属性和剩余预算。
- `EvidenceLedger` 将工具输出规范化为可引用证据，不允许报告直接引用未记录的
  原始模型文本。
- `ReportValidator` 在任何报告进入 Release Center 前校验 schema、证据引用、
  candidate 绑定、风险单调性和状态一致性。

### 4.3 权威性边界

以下模块保持唯一权威来源：

| 责任 | 权威模块 | Agent 能做什么 |
| --- | --- | --- |
| ETL 完成、generation 健康 | `publicationworkflow`/generation store | 读取结果、解释 blocker |
| exact candidate | `publicationworkflow` | 读取并绑定，不得选择冲突权威版本 |
| 权限和租户隔离 | `auth`、`knowledgecatalog` | 在工具调用前请求受限读取 |
| 审批人数与审批人 | `releasecenter` approval policy | 提升风险/建议人工，不得降低门槛 |
| 最终发布 | `publicationworkflow.PublishApproved` | 不可调用 |
| 审计记录 | PostgreSQL/audit store | 追加 Agent 证据，不能覆盖权威状态 |

## 5. Agent 执行模型

### 5.1 审查任务状态

```text
created
  -> preparing
  -> planning
  -> tool_pending
  -> observing
  -> planning
  -> report_validating
  -> completed | needs_info | manual_review | failed
```

任何非终态都可转为 `cancelled` 或 `failed`。工具执行前先持久化调用意图；工具
结果、证据和状态转移随后持久化。进程崩溃后按同一 idempotency key 恢复未完成调用，
不得让 Planner 重复产生不同副作用（首版工具均为只读）。

### 5.2 单步决策契约

Planner 每轮只能返回一个经过 schema 校验的决策：

```json
{
  "type": "tool_call | continue | final | needs_info | manual_review",
  "thought_summary": "面向审计的简短理由，不作为权限依据",
  "tool_name": "registered_tool",
  "arguments": {},
  "claim_ids": ["claim-1"]
}
```

`thought_summary` 只用于可观测性，不保存或展示不必要的私有链式思考。Agent 不能
返回任意代码、URL、SQL、shell 或未注册工具名称。`final` 只有在报告构造器能够
为结论补齐证据时才有效。

### 5.3 终止条件

Agent 可在以下任一条件满足时结束：

- 所有强制检查已完成，报告的 claims 均有证据；
- 证据相互冲突且复核预算耗尽，转 `needs_info`/`manual_review`；
- 工具或模型不可用，转 `failed` 并创建人工例外；
- 达到步骤、token、时间、工具调用次数或成本预算，fail-closed；
- 确定性门禁已经阻断，Agent 可补充解释，但不能把状态改成可发布。

## 6. 首个 Review Tool Registry

首个 MVP 只注册只读、租户绑定、exact-candidate 绑定的工具：

| 工具 | 作用 | 输出必须包含 |
| --- | --- | --- |
| `get_review_context` | 读取治理字段、空间、权限、ETL 状态和政策版本 | context evidence |
| `get_exact_candidate_chunks` | 分页读取当前 candidate 的 chunk | chunk ID、版本、generation |
| `scan_sensitive_data` | DLP/规则扫描密码、密钥、身份证、手机号等 | finding、位置、chunk refs |
| `scan_prompt_injection` | 检测忽略指令、系统提示词窃取、工具诱导等模式 | finding、chunk refs |
| `compare_candidate_versions` | 对比当前 candidate 与指定历史版本 | diff evidence、版本绑定 |
| `verify_policy_requirements` | 读取适用企业政策和强制检查项 | policy refs、missing checks |
| `verify_claim_evidence` | 校验报告 claim 是否被 exact chunks/政策/工具输出支持 | claim verdict、evidence refs |

工具的共同约束：

- 必须显式接收 `tenant_id`、`document_id` 和 exact candidate binding；
- 只能读取当前任务允许的版本和空间；
- 不能发起任意检索扩大租户或版本范围；
- 输出大小、分页、延迟和成本有上限；
- 结果进入 `EvidenceLedger` 后才可被 Planner 或报告引用；
- 工具注册表拒绝任何未声明 `read_only=true` 的首版审查工具。

后续工具（外部制度库、法规检索、跨文档冲突检查）必须单独设计数据驻留、来源
可信度、缓存、供应商失败和引用稳定性，不得直接加入 MVP。

## 7. 证据链与报告

### 7.1 证据对象

每个证据对象包含：

```json
{
  "evidence_id": "ev-123",
  "source_type": "chunk | metadata | policy | tool_result | version_diff",
  "candidate": {"document_version_id": "...", "generation_id": "...", "digest": "..."},
  "locator": "chunk-42",
  "content_digest": "sha256:...",
  "excerpt": "最小必要摘录",
  "tool_call_id": "tool-7",
  "observed_at": "..."
}
```

报告只能引用 `EvidenceLedger` 中存在且 candidate 完全匹配的 `evidence_id`。旧
generation、旧 digest 或未知 chunk 引用必须使报告校验失败。

### 7.2 Findings 与结论

每个 finding 必须包含 code、severity、summary、claim、evidence refs、confidence、
recommended action 和是否阻断。模型可以提出 finding，但不能伪造来源；服务端
只接受已验证的 evidence refs。风险合并采用单调规则：确定性高风险、政策强制阻断
和有效模型高风险只能提升最终风险，不能被后续模型输出降低。

### 7.3 报告状态

```text
completed       Agent 完成强制检查且报告证据完整
needs_info      证据不足或政策要求缺少输入，不能进入普通发布审批
manual_review   模型/工具无法可靠判断，需要人工例外
failed          执行、契约、权限、持久化或证据校验失败
```

`completed` 不等于“自动发布”或“合规通过”；审批策略和确定性发布门禁仍独立
执行。

## 8. 安全与治理

### 8.1 Prompt injection

- 文档内容、chunk、历史文本和工具返回值一律作为不可信数据传给 Planner；
- system/developer 指令与文档数据使用明确结构分隔；
- 文档不得修改工具清单、权限、预算或终止条件；
- 检测到注入时记录 finding，Agent 可继续做安全检查，但不能按普通 `publish`
  建议结束；
- 不把完整系统提示词、密钥、内部策略未授权片段放入文档上下文。

### 8.2 权限与租户

Agent 使用服务端生成的 review principal，不接受模型生成的 tenant/user/role。
所有工具调用重新校验任务 tenant、document、candidate 和 capability。跨租户、跨
空间、跨版本读取返回统一失败，不泄露存在性。

### 8.3 预算与 DoS

每次任务持久化并强制执行：最大步骤、最大模型 token、最大 chunk 数、最大工具调用
数、总时限和可选成本上限。达到任何上限转人工或失败，不允许无限规划循环。

### 8.4 可恢复性与幂等

复用现有 Agent Orchestrator 的 lock、fencing、version、resume 和 timeout。工具
调用 key 为 `review:{run_id}:{step_index}:{tool_name}`；首版工具只读，重复调用可以
按 key 返回相同快照或明确的重读证据，不产生发布副作用。

## 9. 持久化设计

优先复用现有 Agent Run/Step 表和 `ReviewReport`，只增加审查领域必需字段：

- `review_runs`: review goal、candidate binding、policy version、budget、state；
- `review_plan_steps`: 每次 planner decision 的类型、工具、参数 digest、状态；
- `review_tool_calls`: tool name、authorization result、request/response digest、
  latency、error、idempotency key；
- `review_evidence`: evidence object、source locator、candidate binding、retention；
- `review_claims`: claim、severity、confidence、evidence refs、validation state；
- `review_reports`: 继续保存最终业务投影和 model/prompt/policy 版本。

如果现有 Agent Step 能完整承载 plan/tool/evidence 关系，优先扩展 JSONB 和索引，
避免平行状态机。任何新增表必须保留 tenant、candidate 和 immutable audit 约束。

## 10. 迁移策略

### 阶段 A：底座与确定性安全门禁

- 保留现有 deterministic eligibility、内容规则扫描、exact-candidate 校验和审批；
- 将固定 `ReviewPlanner` 适配到新的 `ReviewAgent` seam；
- 把现有规则扫描包装为只读工具，先证明工具契约和证据账本。

### 阶段 B：Agent MVP（shadow mode）

- 新 Agent 与旧固定流程并行运行，但新 Agent 结果不改变审批或发布；
- 比较风险、findings、证据完整率、步骤数、延迟、token 和人工转交率；
- 任一租户或 candidate 出现越权、证据漂移或不可解释结论，自动回退人工例外。

### 阶段 C：受控 canary

- 由配置选择少量租户/空间进入新 Agent；
- 只允许新 Agent 提升风险或转人工，不能降低旧门禁；
- 连续通过完整业务矩阵、模型评测和恢复测试后扩大范围。

### 阶段 D：默认启用

- 新 Agent 成为默认 `ReviewAgent` adapter；
- 旧固定流程保留为 deterministic fallback 和审计对照，不再作为主审查路径；
- 旧报告字段和历史 run 保持只读兼容。

## 11. 分阶段实施计划

### P2.4-R1A：设计与接口冻结

交付 `ReviewRequest`、`ReviewReport`、工具 schema、状态机、证据 schema、预算和
失败码；更新技术设计、数据库迁移草案和验收矩阵。

### P2.4-R1B：ReviewAgentRuntime

实现 Planner 决策循环、ToolGuard、EvidenceLedger、ReportValidator、断点恢复和
确定性测试 planner。此阶段不连接真实模型，不改变发布策略。

### P2.4-R1C：只读审查工具

实现并测试七个 MVP 工具，所有工具通过统一 capability 和 candidate binding seam。
为每个工具提供 in-memory adapter 和生产 PostgreSQL/Qdrant/文档 store adapter。

### P2.4-R1D：模型 Planner 与报告生成

将现有 OpenAI-compatible planner 改造成 review-specific planner：输入为审查目标、
已验证观察和工具清单，输出单步决策；禁止直接生成无证据结论。增加模型超时、非法
工具、非法 JSON、冲突证据、提示注入和预算耗尽测试。

### P2.4-R1E：shadow/canary 验收

执行离线评测、真实 HTTP 矩阵、租户隔离、重启恢复、旧 candidate 拒绝、幂等、
成本/延迟和人工例外验收，保留可复核 evidence bundle。

### P2.4-R5：编排扩展

R1 稳定后再做审批通知、失败补偿、外部工作流和多工具 Saga。任何副作用工具必须
先通过独立的 approval、idempotency 和 compensation 设计。

## 12. 验收矩阵

除现有发布中心矩阵外，新 Agent 必须覆盖：

| 类别 | 必须验证 |
| --- | --- |
| 动态规划 | 正常文档走不同有效工具路径；证据不足时追加工具；冲突时复核 |
| 工具安全 | 未注册工具、非法参数、跨租户、跨版本、越权 capability 均拒绝 |
| 证据完整 | finding/claim 缺证据、未知 chunk、旧 digest、伪造 locator 均 fail-closed |
| 注入防护 | 文档诱导调用工具、泄露 prompt、修改策略/预算均不影响执行权限 |
| 失败处理 | timeout、HTTP 错误、模型非法 JSON、工具错误、数据库故障进入失败/人工例外 |
| 恢复幂等 | crash 后恢复同一步；重复工具调用不产生新副作用；旧锁不能写入 |
| 预算治理 | max steps、tokens、chunks、calls、duration、cost 任一超限均停止 |
| candidate 绑定 | version/generation/revision/digest/count 任一变化使报告和审批失效 |
| 业务闭环 | internal 单审、confidential 双审、高风险升级、拒绝、人工例外、跨租户 |
| 迁移 | shadow 结果可对比；canary 可回退；旧报告可读取；无双重发布 |

## 13. 可观测性与运营指标

必须按 tenant、document ID hash、review policy version 和 model version 记录有限标签：

- review outcome、manual-review rate、evidence-complete rate；
- planner steps、tool calls、tool error rate、budget exhaustion；
- p50/p95 latency、input/output tokens、estimated cost；
- candidate mismatch、cross-tenant denial、prompt-injection finding；
- shadow disagreement、canary fallback 和 recovery success。

原始文档内容、密钥、完整 prompt 和敏感 finding 摘录不得进入普通指标标签；详情
保留在受控审计存储并遵守租户隔离和保留策略。

## 14. 关键决策与待确认项

本方案推荐以下默认值：

- 首版只读工具，不开放任何副作用工具；
- 每次最多 8 步、32 次工具调用、固定 chunk/token/时间预算；
- 允许模型选择工具，但不能选择权限、tenant、candidate 或审批结果；
- 证据不足默认 `needs_info`，模型/工具异常默认 `manual_review` 或 `failed`；
- 先 shadow，再 canary，最后默认启用；
- 旧固定流程作为 deterministic fallback，不作为长期主 Agent。

需要业务责任人确认的输入：审查政策分类、各文档类型强制检查项、风险等级定义、
人工例外 SLA、报告保留期、模型供应商/部署位置、成本上限和 shadow/canary 租户。

## 15. 方案结论

本方案不推倒现有 Agent Orchestrator，而是在其上增加一个真正有深度的
`ReviewAgentRuntime` 模块：调用方只看到一个版本绑定的审查接口，复杂的规划、
工具治理、证据和恢复都隐藏在模块内部。确定性门禁与发布审批继续作为外部权威，
从而同时获得 Agent 的动态审查能力和企业治理所需的可控、可审计、可回退特性。
