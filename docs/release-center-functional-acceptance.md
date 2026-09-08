# 知识发布中心功能验收规范

这是知识发布中心每次代码变更后的业务功能验收门禁。它与单元测试、
数据库集成测试和 Web 契约测试不同，必须验证“文档属性 → Agent 预审 →
审批策略 → 发布结果”的完整行为链。

## 功能矩阵

| 场景 | 输入/文档属性 | 预期 Agent 结果 | 预期审批 | 预期终态 |
| --- | --- | --- | --- | --- |
| 普通受管文档 | `internal`、ETL 完成、治理字段完整、精确候选健康 | `completed`，建议 `publish` | 1 位管理员 | 一次批准后精确版本发布 |
| 机密文档 | `confidential`，其他条件同上 | 成功或低风险不能降低策略 | 2 位不同管理员 | 第一票不发布，第二票后发布；发起人自审拒绝、重复相同决定幂等、冲突决定拒绝 |
| Agent 判定高风险 | `internal`，Agent `high/critical` | 预审成功但风险升级 | 2 位不同管理员 | 第一票不发布，第二票后发布 |
| Agent 运行异常 | Agent 返回 error/超时/不可用 | `failed`，建议 `manual_review`，风险归一化为 high | 人工例外路径；两位管理员均需填写理由 | 第一票仍为人工例外，第二票后发布 |
| Agent 失败状态 | Agent 无 error 但返回 `status=failed/error` | 必须按失败处理 | 不得进入普通成功预审 | `manual_exception` |
| 确定性门禁阻塞 | 缺责任人、无生效日期、非受管空间、ETL 未完成或无精确候选 | 不应触发成功预审/发布 | 不生成可发布审批任务 | `needs_info` 或 `review_blocked` |
| 版本在审批期间变化 | generation/version/revision/digest 任一变化 | 旧报告失效 | 旧审批不可发布 | `needs_info`，保留审计记录 |
| 预审报告过期 | 待审批/待补齐报告超过 `RELEASE_REVIEW_TTL` | 报告 `expired`，请求 `needs_info` | 不得用过期报告批准 | 同一精确候选自动重审；已发布/已拒绝证据不改写 |
| Agent 证据不完整 | status、recommendation 或 risk 缺失/非法 | fail-closed，标记失败 | 进入人工例外，不能按成功预审审批 | 需人工核对后处理 |
| 内容级敏感信息 | exact candidate 切块包含密码、API key、身份证号或手机号 | 生成带 chunk 引用的 `sensitive_data_detected` finding；风险升为 high | internal 阻断；confidential 升级双人复核 | 不得静默放行 |
| 内容级提示词注入 | 切块包含“忽略之前指令/输出系统提示词”等模式 | 生成 `prompt_injection_detected` finding，建议 `needs_info` | 不进入普通可发布审批 | 人工处理 |
| 自主 Review Agent 成功 | 配置 RAG Query 同源模型，返回合法结构化结果 | 保留模型、Prompt 版本和 exact chunk finding；仅允许风险升级 | 按确定性最低策略审批 | 结果可审计且候选绑定 |
| 自主 Review Agent 失败 | 超时、HTTP 错误、非法 JSON、非法枚举或未知 chunk 证据 | 预审 `failed`、风险 `high`、建议 `manual_review` | 人工例外；不得按成功预审发布 | fail-closed |
| 非管理员或发起人自审 | 普通用户、或请求发起人本人 | 不适用 | 拒绝决定请求 | 不改变发布状态 |
| 未认证和跨租户访问 | 无凭证或其他租户管理员访问 | 不适用 | 返回 401/租户隔离 | 不泄露记录或证据 |

## 必须验证的断言

1. Agent 结果只是版本绑定的审核证据，不能改变权限、元数据、审批人数或发布状态。
2. `internal + low + Agent completed` 才是单人审批的普通路径；`confidential`
   或 `high/critical` 必须双人且不同身份。
3. Agent error 和 `status=failed/error` 都不能被当作预审成功。
4. 确定性门禁失败不能因为 Agent 建议 `publish` 而放行。
5. 最终批准前必须重新校验同一个 exact candidate；候选变化时拒绝发布。
6. 同一管理员重复提交相同决定必须幂等，提交不同决定必须冲突失败。
7. 当前预审负责受管发布资格、确定性敏感信息/提示词注入扫描和 Agent 证据编排；
   不得把这组有限规则描述成完整的内容语义、隐私或合规分类审查。未来扩展时必须
   新增独立输入、输出 schema、证据引用和对应功能矩阵。
8. 内容 findings 必须引用 exact candidate 的 chunk ID；旧 generation 或旧 version
   的内容不得进入当前 review report。
9. Web 详情必须展示预审状态、风险、建议、预审时间、Prompt 版本以及每条
   finding 的类型、严重度和 chunk 证据引用；机密敏感内容必须明确提示仍需双人审批。
10. 没有 durable release request 的历史业务记录仍必须通过只读、租户隔离的
    review report seam 查看预审证据；该 seam 不提供审批或发布动作。

## 执行入口

快速功能矩阵（不依赖外部模型）:

```bash
docker run --rm \
  -v "$PWD/services/etl-worker:/app" -w /app \
  golang:1.25-alpine \
  sh -c 'go test ./internal/releasecenter -run "TestCoordinator|TestApproval|TestManual|TestApprovalPolicy|TestProjectOverview" -count=1'
```

完整真实部署验收（包含 HTTP、Kafka、PostgreSQL、索引和恢复）:

```bash
bash scripts/governance-acceptance.sh
```

Agent 预审业务矩阵（独立隔离栈，验证文档属性到审批终态）:

```bash
bash scripts/release-center-functional-acceptance.sh
```

隔离栈当前执行 10 个真实业务场景：普通单审、机密双审、机密敏感双审、
Agent 状态依赖故障与两票人工例外、internal 敏感阻断、提示词注入阻断、
确定性门禁、版本替换失效与旧 finding 隔离、拒绝终态、双租户隔离。故障场景
只通过 Compose 控制面停止并恢复隔离栈 `redis-state`；业务输入和结果仍通过认证
HTTP API 写入及观察，不向生产服务加入测试端点或测试模式。

Token 预算专项验收（真实模型、独立隔离栈）:

```bash
bash scripts/release-center-token-budget-acceptance.sh
```

Review Agent 部署恢复专项验收（隔离栈 Redis/Compose 故障）:

```bash
bash scripts/release-center-review-recovery-acceptance.sh
```

该专项强制 `AGENT_PLANNER_TYPE=auto` 和显式真实 `LLM_ENDPOINT`/`LLM_MODEL`，通过公共
HTTP 上传受管文档并观察发布请求、只读 review report 和 Agent Run。验收必须证明累计
`tokens_used` 超过 `max_token_budget` 时 Run 以 `token_budget_exceeded` 失败、请求进入
`manual_exception`、报告建议 `manual_review` 且每个 Planner step 的 usage 可加总回 Run
累计值。报告默认保存在 `artifacts/release-center-token-budget-acceptance/`，内容经过
凭据和敏感字段脱敏。

恢复专项在隔离栈中用可暂停的 Review Planner 替身制造“已持久化至少一个工具步骤”的窗口，
然后通过 Compose 控制面 `kill query-api` 并停止/恢复 `redis-state`。业务输入和终态仍通过
认证 HTTP 观察。验收必须证明恢复后仍是同一条 Review Run、首个工具步骤的幂等键不变、
四个只读工具不重复执行，并且最终进入可审批状态。报告默认保存在
`artifacts/release-center-review-recovery-acceptance/`，不得写入文档正文、完整 prompt
或凭据。该门禁验证崩溃恢复，不替代真实模型四场景验收。

真实模型四场景验收（独立隔离栈）:

```bash
bash scripts/release-center-real-model-scenarios-acceptance.sh
```

该专项强制 `AGENT_PLANNER_TYPE=auto` 和显式真实 `LLM_ENDPOINT`/`LLM_MODEL`，通过公共
HTTP 上传四类受管文档：普通、敏感信息、提示词注入、证据不足。验收必须证明真实模型
完成四个只读工具，记录延迟和累计 token，并且：普通文档进入 `approval_pending`/`publish`；
敏感信息和提示词注入保留带 chunk 引用的确定性 finding 并转 `needs_info`；占位/证据不足
文档不得建议 `publish`。报告默认保存在
`artifacts/release-center-real-model-scenarios-acceptance/`，不得写入文档正文、完整 prompt
或凭据。

生产适配器不会自然生成的上游异常证据由真实 HTTP handler 功能测试覆盖：

```bash
docker run --rm \
  -v "$PWD/services/etl-worker:/app" -w /app \
  golang:1.25-alpine \
  sh -c 'go test ./cmd/api -run "TestReleaseCenterHTTP" -count=1'
```

Review Agent 重启/幂等代码路径由 `internal/agentapi` 的
`TestAutonomousReviewResumesPersistedRunWithoutDuplicatingSteps` 覆盖：先持久化
一个已完成的 review tool step，再由新的 Service/Orchestrator 通过生产预审入口自动继续同一 Run，验证 Run ID、
观察证据、工具顺序和稳定幂等键不变，不新建第二条预审链。
同一专项还验证 `TestAutonomousReviewRejectsCandidateDriftDuringResume`：恢复期间
generation/version 改变时必须 fail-closed，不能切换到新候选继续审查。
部署故障证据必须另外运行 `scripts/release-center-review-recovery-acceptance.sh`，不能只靠这些单测关闭 R1。

该层注入 error、显式 `failed/error`、缺失 status、非法 recommendation/risk，
并验证 internal high-risk 双管理员策略和请求发起人自审拒绝。它使用生产 HTTP
handler 和协调器，只替换上游 `ReviewAdapter`，避免为了验收引入生产测试后门。

每次修改 `releasecenter`、Agent 预审适配器、publication workflow 或发布中心
页面，都必须执行快速功能矩阵和 Web/Python 全量测试；发布前还必须保留完整
部署验收报告。禁止只以静态检查或单一 happy path 作为完成依据。

## 当前未实现边界

首个版本的 Agent 预审是受限的发布资格审查，包含确定性内容安全扫描，但不是完整的
语义、隐私或合规分类器。
以下事项不属于当前已完成基线，必须单独立项并补充输入、输出、证据和验收定义：

- 完整语义/隐私/合规审查及更广泛的模型化内容分类；
- 企业 IdP 组同步、委托审批、定时升级和审批撤权；本地租户审批组与可配置策略已由 P2.4-R2 实现；
- Agent 状态契约的真实 PostgreSQL 集成验收（协调器已将 `success` 规范化为 `completed`）；
- 不内嵌 Camunda/Temporal 等 BPM 引擎。外部引擎通过治理通知 webhook 收事件，并通过
  `POST /v1/release-center/workflow/decision` 回写到现有幂等 `Decide`。
  审批通知 outbox、企业微信/钉钉投递、workflow callback 与副作用反向补偿已由
  P2.4-R5 交付，验收以单测和快速矩阵为准。

在这些事项完成前，不得把当前规则扫描、风险升级或管理员审批结果描述为完整安全或合规结论。

P2.4-R3 的实现边界：预审报告默认 7 天过期；过期待审批/待补齐记录进入 `needs_info` 并自动重审同一精确候选。
已发布或已拒绝记录保留原报告。过期报告不得用于批准。Web 以 `review_expired` 提示等待重审。

P2.4-R2 的实现边界：管理员可通过 `POST/GET /v1/release-center/approval-groups`、
`POST /v1/release-center/approval-groups/{id}/members` 和
`POST/GET /v1/release-center/approval-policies` 配置租户内审批组与策略；发布请求
保存命中的策略和审批组，并在每次决定时重新校验成员是否仍处于启用状态。策略
只能提高确定性最低审批要求，不能绕过精确候选复验、租户隔离或请求人自审限制。

## 当前实施顺序

当前主线是企业级 Agent 审计功能，不包含通用生产准入、企业身份部署或 OCR 验收。
实施顺序固定为：P2.4-R1（自主预审 Agent，已完成）→ P2.4-R5（通知、补偿和外部编排，已完成）→
P2.4-R3（报告生命周期，已完成）。P2.4-R2（审批组与策略）已完成。
P1.9、P2.3、P2.5、P2.6 和 OCR 不得在未得到用户重新指定时改变该顺序。
