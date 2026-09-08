# P2.4-R5 Agent 周边编排

最后核验：2026-09-08。

## 目标

在 R1 自主预审和 R2 审批组已经闭环之后，补齐企业落地所需的周边编排：审批事件必须可投递、失败副作用必须可补偿、外部工作流必须有稳定接入契约。本切片不嵌入 Camunda/Temporal 之类引擎，也不把当前只读预审描述成带副作用的多系统事务。

## 三项评估

| 能力 | 结论 | 本切片 |
| --- | --- | --- |
| 异步审批通知 | 发布中心已能进入 `approval_pending` / `manual_exception`，但管理员只能轮询页面。复用现有企业微信/钉钉适配器，先做 PostgreSQL outbox。 | 已实现 |
| 多工具 Saga / 失败补偿 | Orchestrator 原先只补偿“当前失败工具”的部分副作用。完整反向补偿只适用于注册了 compensator 的副作用工具；只读 Review 工具不参与。`publish_document` 保持幂等发布，不自动撤销。 | 已实现反向补偿 |
| 外部工作流引擎 | 不内嵌引擎。同一 outbox 事件就是出站契约；入站继续走现有幂等 `Decide` API。 | 契约已定义，引擎适配后置 |

## 通知设计

- 表：`governance_notification_outbox`。按 `dedupe_key` 幂等写入。
- 事件：`release.request.opened`、`release.decision.recorded`、`release.request.state_changed`。
- 载荷只允许租户、请求、文档、状态、风险枚举、审批组、决定人等标识；禁止 findings、summary、正文、prompt 和密钥。
- 投递失败可重试，不能回滚预审或审批。未配置 webhook 时跳过外发并关闭 outbox 行。
- Query API relay POST 到 `alert-webhook-service /notifications`。无企业通道时该接口返回 202，避免本地演示堆积重试。

## Saga 设计

运行失败、取消、审批拒绝或超时时，按相反顺序补偿已经 `completed` 且注册了 compensator 的副作用步骤。补偿必须可幂等。补偿失败记录在 Step 上，不把运行重新打开。

## 验收

- 打开待审批请求会写入一条内容安全的 outbox 事件；重复保存不重复投递。
- 拒绝/发布/候选失效会追加状态事件。
- webhook 失败后 relay 重试，成功后标记已投递。
- 两个成功副作用工具之后第三个失败时，补偿顺序为失败工具（若需要）再反向补偿前序工具。
- 发布中心快速功能矩阵保持通过；通知故障不得改变审批人数或发布结果。
