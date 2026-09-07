# Agent 预审架构与实施方案

## 1. 目标

将 Knowledge Release Center 当前“固定流程 + 单次模型判断”的预审改造成最小自主
闭环：模型能够根据已观察到的文档内容和检查结果，自主决定下一次调用哪个受控工具，
或结束并提交有证据的审查报告。

首版目标不是建设完整的 Agent 平台，而是在现有 Agent Orchestrator 上完成一个真实、
可控、可验证的多步预审 Agent。

## 2. 核心原则

- **自主但受限**：模型决定检查顺序和是否继续，服务端决定工具、权限、预算和候选范围。
- **只读**：首版 Agent 不能审批、发布、修改权限、修改元数据或调用任意外部系统。
- **证据优先**：每条 finding 必须引用当前 exact candidate 的 chunk 或工具结果。
- **安全门禁外置**：ETL、candidate、租户、审批人数和最终发布仍由现有确定性模块负责。
- **复用现有底座**：不新建第二套编排器、状态机、工具权限或运行记录。
- **失败关闭**：模型、工具、证据校验、预算或持久化异常均转人工处理，不静默放行。

## 3. 当前实现与目标差异

当前流程：

```text
固定 ReviewPlanner
  -> 固定 assess_publication 工具
  -> 固定规则扫描
  -> 单次 semantic reviewer
  -> 固定合并结果
```

目标流程：

```text
Release Center
  -> exact candidate 确定性门禁
  -> 现有 Agent Orchestrator
       -> Review LLM Planner
       -> 选择一个已注册只读工具
       -> 观察工具结果
       -> 决定继续调用工具或提交报告
  -> 服务端校验报告和证据
  -> 现有审批与发布门禁
```

关键变化只有一个：用模型驱动的多步决策替换固定 `ReviewPlanner`。其他企业治理能力
继续复用现有实现。

## 4. 最小架构

### 4.1 对外 seam

发布中心只依赖一个小接口：

```go
type ReviewAgent interface {
    Review(context.Context, ReviewRequest) (ReviewReport, error)
}
```

`ReviewRequest` 只包含：

- tenant、document、knowledge space 和权限；
- exact candidate：version、generation、revision、digest、chunk count；
- 确定性 eligibility 结果；
- 最大步骤、最大 token 和总超时。

`ReviewReport` 沿用现有发布中心结构，包含：

- exact candidate 绑定；
- status、recommendation、risk、summary；
- findings 及其 evidence refs；
- Agent Run ID、model、prompt version 和时间。

不在首版引入独立的 plan、claim、evidence 或 tool-call 领域模型。

### 4.2 复用关系

| 能力 | 直接复用 |
| --- | --- |
| 多步执行和恢复 | `internal/agent.Orchestrator` |
| 工具 schema 和参数校验 | `internal/agent.Registry` |
| 工具权限 | `internal/agent.Authorizer` |
| Run/Step 持久化、锁和 fencing | 现有 Redis Agent Store |
| 超步数和超时 | 现有 Agent Options |
| exact candidate 和发布 | `publicationworkflow` |
| 审批和租户策略 | `releasecenter` |
| 最终业务报告 | 现有 `ReviewReport` PostgreSQL 记录 |

首版不新增 `ReviewAgentRuntime`、`ToolGuard`、`EvidenceLedger` 等模块。只有在现有
接口无法表达必要行为并有测试证据时，才新增 seam。

## 5. Agent 决策循环

### 5.1 Planner 输出

Review Planner 每轮只能返回两种结构化决定之一：

```json
{"type":"tool_call","tool_name":"get_exact_candidate_chunks","arguments":{"offset":0,"limit":20}}
```

或：

```json
{
  "type":"final",
  "report":{
    "status":"completed",
    "recommendation":"publish",
    "risk_level":"low",
    "summary":"未发现阻断项",
    "findings":[]
  }
}
```

模型不能生成 tenant、document ID、candidate、权限或审批信息。这些值由服务端从
`ReviewRequest` 绑定到每次工具调用。

### 5.2 执行规则

1. 服务端先执行现有 deterministic eligibility；没有 exact candidate 时不启动 Agent。
2. Orchestrator 创建版本绑定的 review run，并将最小审查上下文交给 Review Planner。
3. Planner 选择一个注册工具；Registry 和 Authorizer 校验后执行。
4. 工具结果作为下一轮 observation 持久化在现有 Step 中。
5. Planner 根据 observation 继续调用工具，或提交最终结构化报告。
6. 服务端校验报告的枚举、风险单调性及 evidence refs。
7. 合法报告进入现有审批流程；任何异常进入 `manual_review` 或 `failed`。

这形成最小自主闭环：**计划一个动作 → 使用工具 → 观察 → 再计划或结束**。

### 5.3 终止条件

- Planner 提交合法且证据完整的报告；
- 达到最大步骤、token 或总超时；
- 模型或工具失败；
- 模型选择未注册工具、参数非法或引用未知证据；
- exact candidate 在审查期间变化。

除第一种情况外均 fail-closed，不进入普通发布审批。

## 6. 首版四个只读工具

| 工具 | 用途 |
| --- | --- |
| `get_review_context` | 读取治理字段、权限、知识空间和 eligibility blocker |
| `get_exact_candidate_chunks` | 分页读取当前 exact candidate 的 chunks；模型按需选择页 |
| `scan_sensitive_data` | 对当前 candidate 执行确定性敏感信息扫描 |
| `scan_prompt_injection` | 对当前 candidate 执行提示词注入扫描 |

共同约束：

- tenant、document 和 candidate 由服务端绑定，不接受模型指定；
- 工具只能读取本次任务的 exact candidate；
- 工具输出携带稳定 chunk ID 或 Step ID，供报告引用；
- 单次返回大小、分页和总调用次数受限；
- 工具保持只读和幂等。

两项确定性扫描始终覆盖全部 exact-candidate chunks；模型无需在有限上下文中逐页装载
整篇大文档，但必须读取至少一页内容，并可根据观察继续读取其他页面。

版本对比、企业政策库、外部法规检索、跨文档冲突检查和独立 claim 验证不属于首版。

## 7. 最小证据规则

首版不建立独立 Evidence Ledger。证据来源只有两类：

- `chunk:<chunk_id>`：来自 `get_exact_candidate_chunks` 或扫描工具；
- `step:<step_id>`：来自当前 Agent Run 的已完成工具 Step。

报告保存前必须验证：

1. 引用存在于当前 Run；
2. chunk 属于当前 version 和 generation；
3. finding 有非空 code、severity、summary 和至少一个 evidence ref；
4. 模型不能降低确定性规则已经给出的风险；
5. `publish` 建议不能覆盖 eligibility blocker 或注入阻断。

现有 `ReviewReport.Findings` 足以承载首版结果。只有真实需求证明需要跨报告复用、
独立保留或复杂 claim 图时，才新增证据表。

## 8. 安全边界

- 文档和工具结果始终作为不可信数据，不得改变 system 指令、工具清单或预算。
- Review Planner 只能看到本次允许的四个工具。
- Agent principal 由服务端创建，模型不能决定身份和 capability。
- 所有工具在执行时重新校验 tenant 和 exact candidate。
- Agent 没有审批或发布工具；即使模型要求调用也会被 Registry 拒绝。
- 原始文档、完整 prompt、密钥和敏感内容不得写入普通日志或指标标签。
- 默认最多 8 个 Agent Step；总工具调用次数受同一上限约束，不另设 32 次调用预算。

## 9. 持久化

首版不新增审查运行表：

- 使用现有 Agent Run/Step 保存 Planner 决定、工具参数、结果和错误；
- 使用现有 `ReviewReport` 保存最终业务结果；
- ReviewReport 保存 Agent Run ID，用于从报告追踪完整执行过程；
- exact candidate 继续保存在 ReviewReport 和 ReleaseRequest；
- 复用现有 lock、fencing、version 和 resume 语义。

如现有 Step 不能保存最终结构化 report，只扩展 Step 的结果 JSON，不创建平行状态机。

## 10. 实施步骤

### 步骤 1：Review Planner

- 用 review-specific LLM Planner 替换固定 `ReviewPlanner`；
- 复用 RAG Query 的模型配置；
- 支持 `tool_call` 和带结构化 report 的 `final`；
- 校验未注册工具、非法参数和非法报告。

完成条件：确定性测试中，Planner 能根据不同 observation 选择继续检查或结束，而不是
按固定顺序执行。

### 步骤 2：四个只读工具

- 将现有文档读取和两类规则扫描包装成 Agent tools；
- 所有 handler 从 run context 获取 tenant/document/candidate；
- 工具结果返回稳定 chunk/step 引用；
- 验证越权、跨版本、分页和幂等行为。

完成条件：Agent 能完成至少一次“读取内容 → 发现风险 → 再调用扫描工具 → 提交报告”的
真实多步路径。

### 步骤 3：接入发布中心

- 让 `ReviewPublicationReport` 返回经验证的 ReviewReport，而不是只取 eligibility assessment；
- 删除发布适配器中的单次 semantic reviewer 固定调用；
- 保留 deterministic eligibility、风险下限和现有审批逻辑；
- 继续使用现有 exact-candidate stale 检查。

完成条件：Agent 的动态报告进入现有单审、双审或人工例外路径，Agent 仍无法直接发布。

### 步骤 4：验收

- 运行现有发布中心完整业务矩阵；
- 新增动态续查、未注册工具、跨租户/版本、未知证据、模型错误、预算耗尽和恢复测试；
- 使用真实模型验证正常、敏感信息、提示注入和证据不足四类文档；
- 记录延迟、token 和人工转交结果。

完成条件：功能矩阵、Go/Python/Web 测试和真实模型验收全部通过。

## 11. MVP 完成定义

满足以下条件即可结束首版，不要求同时建设未来平台能力：

- Agent 根据 observation 决定下一工具或结束，存在真实的多步反馈循环；
- 只注册四个只读工具，且 tenant/candidate 由服务端强绑定；
- findings 全部引用当前 Run 的有效 chunk 或 Step；
- 步骤、时间和 token 预算可强制终止运行；
- 崩溃后复用现有 Orchestrator 恢复，不重复产生副作用；
- 模型或工具失败进入人工例外；
- 现有审批、发布、租户和 exact-candidate 门禁全部保持有效；
- 完整验收矩阵和真实模型场景通过。

## 12. 后续范围

以下能力仅在 MVP 验证后按业务价值立项：

- 版本差异审查和跨文档冲突检查；
- 企业政策库和外部法规检索；
- 独立 Evidence Ledger、claim 图和证据生命周期；
- shadow/canary 自动化与高级模型评测平台；
- 异步通知、外部工作流和带副作用的 Saga；
- 报告 TTL、自动重审和历史证据清理。

## 13. 结论

最小方案只改造预审决策方式，不重建 Agent 基础设施：复用现有 Orchestrator，让模型
在四个只读工具之间完成“计划—工具—观察—再计划/报告”的闭环，再由现有服务端门禁
验证证据并控制审批发布。这已经满足“真正可预审的 Agent”定义，同时把新增模块、
数据表和上线编排留到实际需求出现之后。
