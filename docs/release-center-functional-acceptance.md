# 知识发布中心功能验收规范

这是知识发布中心每次代码变更后的业务功能验收门禁。它与单元测试、
数据库集成测试和 Web 契约测试不同，必须验证“文档属性 → Agent 预审 →
审批策略 → 发布结果”的完整行为链。

## 功能矩阵

| 场景 | 输入/文档属性 | 预期 Agent 结果 | 预期审批 | 预期终态 |
| --- | --- | --- | --- | --- |
| 普通受管文档 | `internal`、ETL 完成、治理字段完整、精确候选健康 | `completed`，建议 `publish` | 1 位管理员 | 一次批准后精确版本发布 |
| 机密文档 | `confidential`，其他条件同上 | 成功或低风险不能降低策略 | 2 位不同管理员 | 第一票不发布，第二票后发布 |
| Agent 判定高风险 | `internal`，Agent `high/critical` | 预审成功但风险升级 | 2 位不同管理员 | 第一票不发布，第二票后发布 |
| Agent 运行异常 | Agent 返回 error/超时/不可用 | `failed`，建议 `manual_review` | 人工例外路径；理由必填 | 仅在人工核对并审批后发布 |
| Agent 失败状态 | Agent 无 error 但返回 `status=failed/error` | 必须按失败处理 | 不得进入普通成功预审 | `manual_exception` |
| 确定性门禁阻塞 | 缺责任人、无生效日期、非受管空间、ETL 未完成或无精确候选 | 不应触发成功预审/发布 | 不生成可发布审批任务 | `needs_info` 或 `review_blocked` |
| 版本在审批期间变化 | generation/version/revision/digest 任一变化 | 旧报告失效 | 旧审批不可发布 | `needs_info`，保留审计记录 |
| Agent 证据不完整 | status、recommendation 或 risk 缺失/非法 | fail-closed，标记失败 | 进入人工例外，不能按成功预审审批 | 需人工核对后处理 |
| 内容级敏感信息 | exact candidate 切块包含密码、API key、身份证号或手机号 | 生成带 chunk 引用的 `sensitive_data_detected` finding；风险升为 high | internal 阻断；confidential 升级双人复核 | 不得静默放行 |
| 内容级提示词注入 | 切块包含“忽略之前指令/输出系统提示词”等模式 | 生成 `prompt_injection_detected` finding，建议 `needs_info` | 不进入普通可发布审批 | 人工处理 |
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
7. 当前预审只负责受管发布资格和 Agent 证据编排；不得把资格复验结果描述成
   已完成的内容语义/敏感信息审查。若未来增加内容审查，必须新增独立输入、
   输出 schema、证据引用和对应功能矩阵。
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

每次修改 `releasecenter`、Agent 预审适配器、publication workflow 或发布中心
页面，都必须执行快速功能矩阵和 Web/Python 全量测试；发布前还必须保留完整
部署验收报告。禁止只以静态检查或单一 happy path 作为完成依据。
