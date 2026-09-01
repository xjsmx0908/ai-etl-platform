# P2.5 F1–F6 阶段总结与可运行验收

## 阶段结论

P2.5 F1–F6 已完成实现、评审、合并和本地可运行验收。该阶段为个人演示环境建立了
provider-neutral 状态化会话、敏感操作重新认证、安全退出及用户会话管理。后续 G–J
不在本阶段范围内，保持延期，不因本结论自动启动。

## 交付映射

| 切片 | PR | 已交付能力 |
| --- | --- | --- |
| F1 | #32 | PostgreSQL 会话模型、状态机、轮换与撤销核心 |
| F2 | #33 | `ps1_` 凭据验证与实时用户权限解析 |
| F3a | #34 | 高风险动作分类、新鲜认证判断与结构化挑战 |
| F3b | #35 | 可信 OIDC 证据及绑定 state/nonce/PKCE 的单次事务 |
| F3c | #36 | HTTP 重新认证、Cookie 编排与原子凭据轮换 |
| F4 | #37 | password/OIDC 初始 `ps1_` 签发 |
| F5 | #38 | 服务端撤销优先的安全退出和可选 IdP logout |
| F6 | #39 | 三会话上限、脱敏列表、稳定淘汰与指定会话撤销 |

## 验收结果

- PR #39 的 9 项 GitHub checks（含 `Required Checks`）全部通过。
- `go test -race ./internal/session ./cmd/api -count=1` 使用真实 PostgreSQL 通过。
- `bash scripts/governance-acceptance.sh` 从公共 HTTP seam 完成 6/6 场景：依赖中断、
  独立审批、替换连续性、批准切换、可恢复删除和审计关联。
- 隔离 Compose 运行验收验证了四次 password 登录、最旧会话 401、三个 `sm1_` 脱敏
  句柄、指定撤销、退出清 Cookie，以及退出后凭据 401。验收栈和卷已清理，原演示栈未改动。

## 适用边界与验收工具收尾

能力默认关闭，仅允许 `ENVIRONMENT=dev`、`IDENTITY_POLICY_PROFILE=personal-demo-v1`
和 `SESSION_CORE_ENABLED=true` 的个人演示配置。本次验证了本地 password 运行时；PD2
demo realm 尚未配置，因此未执行真实 Keycloak 浏览器流程，也不构成 staging/production
可用性声明。

阶段总结时登记的 `bash scripts/e2e-smoke.sh` 直接发布欠账已于 2026-09-01 修复。脚本现在
创建受管知识空间和第二管理员，通过 Agent publication workflow 评估精确版本候选并完成
独立审批；查询和 API/Web 搜索显式绑定同一空间。完整隔离运行已通过上传、Kafka、解析、
向量化、索引、审批、查询、chunks 和可恢复删除至 404，不再使用
`publication_status=published` PATCH 绕过治理。
