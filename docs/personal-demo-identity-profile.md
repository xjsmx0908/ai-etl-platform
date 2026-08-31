# 个人演示身份策略

## 状态与适用范围

Profile ID：`personal-demo-v1`。状态：`DemoApproved`。批准角色：仓库所有者兼 Demo
Owner。适用环境仅为本机 `ENVIRONMENT=dev`，数据必须是虚构身份、组、tenant 和
workload；`demo` 是 profile 用途标签，不是新增的运行环境枚举。

`DemoApproved` 表示下列值可作为实现与本地测试输入，不等于企业 `Approved`、staging
验收或 production 授权。企业[决策登记](enterprise-identity-decision-register.md)中的
51 项继续保持 `Pending`，D0～D7 继续为 `Blocked`。本 profile 不能生成企业 2.0
acceptance manifest，也不能把任何 feature 标成 `staging_passed` 或
`production_enabled`。

演示 IdP 固定为开发者管理的 Keycloak 26.3.3 本地依赖，使用专用 demo realm 和 HTTPS
issuer；它目前不在主 Compose 中，必须在后续切片显式配置。secret 只放 ignored 文件或
Docker secret，仓库只保留示例。功能仍默认关闭，必须显式选择 demo profile 才能启用。

## E：提供方与生命周期

| ID | DemoApproved 值 |
| --- | --- |
| E-01 | Keycloak 26.3.3、专用 `ai-etl-demo` realm；Demo Owner 管理 connector。 |
| E-02 | `demo-keycloak` connector 只映射配置引用 `DEMO_TENANT_ID`；禁止域名、邮箱或组推断 tenant。 |
| E-03 | Keycloak 稳定 `sub` 精确写入 SCIM `externalId`；禁止邮箱或用户名回退。 |
| E-04 | 新用户固定为 `readonly`；仅经平台内 Demo Owner 操作提权。 |
| E-05 | 只存 `userName`、显示名、主邮箱；不存 Keycloak role/group 作为权限。 |
| E-06 | SCIM-only，JIT 关闭；演示用户先 provisioning 再登录。 |
| E-07 | 停用提交目标 5 分钟内；平台收到后在下一请求撤权。 |
| E-08 | 每 24 小时完整对账，漏跑 1 次告警；只经 lifecycle seam 修复。 |
| E-09 | tombstone 保留 365 天；本 profile 不自动物理清理。 |
| E-10 | connector secret 最长 90 天，旧新重叠不超过 24 小时后显式退役。 |

## F：会话安全

| ID | DemoApproved 值 |
| --- | --- |
| F-01 | 联邦演示要求 Keycloak 密码 + TOTP；adapter 只把明确配置的 `acr`/`amr` 组合映射为 `demo-mfa`。 |
| F-02 | 空闲超时 30 分钟；最后活动合并写入不得延长该边界。 |
| F-03 | 绝对寿命 8 小时，不因活动、Cookie 或 IdP 续期延长。 |
| F-04 | 高风险操作要求 10 分钟内的 `demo-mfa` 认证；缺少可信 `auth_time` 时重新认证。 |
| F-05 | Keycloak SSO 空闲 30 分钟、最大 8 小时；通过强制新登录测试验证。 |
| F-06 | Cookie 最长 30 分钟，滚动续期不超过绝对寿命；建立/重认证时轮换。 |
| F-07 | 每用户最多 3 个活跃会话；创建第 4 个时撤销最旧会话并审计。 |
| F-08 | 允许用户查看脱敏的创建/最近活动时间并撤销自己的单个会话。 |
| F-09 | 先原子撤销本地会话，再尝试 RP-initiated logout；不实现 back-channel logout。 |
| F-10 | 仅迁移虚构 demo 用户，一批最多 5 个，观察 30 分钟；回滚恢复已验证的本地 demo 登录。 |

## G：紧急访问

个人演示无法证明真实多人职责分离或独立保管，以下仅验证状态机和失败关闭行为；任何
UI、报告和证据必须显示 `SIMULATION`。

| ID | DemoApproved 值 |
| --- | --- |
| G-01 | 仅允许标记为 demo drill 的“主 IdP 不可用”和“错误联邦配置”两类事件。 |
| G-02 | 两个独立测试签名 key；均由 Demo Owner 控制，因此不构成企业独立保管。 |
| G-03 | 代码要求 2-of-2 不同 key 且操作 key 不得签署；单人持有仍是已知演示限制。 |
| G-04 | Ed25519 challenge 签名，测试 key 每 30 天轮换；禁止 bearer 激活证明。 |
| G-05 | 租约最长 15 分钟、空闲 5 分钟、不允许延期；需要新请求重新激活。 |
| G-06 | 只允许 demo tenant 的 `identity.provider.rollback` 和 `identity.sessions.revoke`。 |
| G-07 | 仅 loopback 管理入口和本机演示终端；不声称独立管理工作站。 |
| G-08 | 写入审计 outbox 后通知本地 webhook；1 分钟未送达显示告警。 |
| G-09 | 每次相关代码变更后运行 canary；至少证明激活、两项动作、到期、撤销和恢复。 |
| G-10 | 脱敏演练证据保留 30 天；Demo Owner 复核并标记 simulation。 |
| G-11 | 仅记录 Docker volume 备份/恢复演练；不替代 PostgreSQL、主机或云平台 DR。 |

## H：组到知识空间授权

| ID | DemoApproved 值 |
| --- | --- |
| H-01 | Keycloak realm 为组权威，使用不可复用的 Keycloak group UUID。 |
| H-02 | 使用只读、分页的 Keycloak Admin REST directory adapter；不宣称 SCIM Groups 兼容。 |
| H-03 | 只支持直接成员；嵌套组和动态组明确拒绝。 |
| H-04 | 每 5 分钟同步，最大陈旧窗口 10 分钟；从 provider 变更起 10 分钟内完成撤权，完成快照后的下一次授权检查生效。 |
| H-05 | 每 connector 最多 100 个映射、每组 1,000 个成员、每页 100；嵌套深度为 0。 |
| H-06 | provider 组永不产生平台 `admin`；manager 映射仅由 Demo Owner 创建。 |
| H-07 | contributor/manager 映射需要独立审计事件；reader 也必须显式创建映射。 |
| H-08 | 组与成员 tombstone、摘要审计保留 365 天；不保存多余原始资料。 |
| H-09 | 每小时完整对账，漏跑 1 次或发现冲突即本地告警，由 Demo Owner 处置。 |
| H-10 | allow 缓存最长 60 秒且每次校验 revision fence；状态未知时回源或拒绝。 |

## I：服务与工作负载身份

| ID | DemoApproved 值 |
| --- | --- |
| I-01 | Keycloak demo realm 签发 workload token；profile 固定为 `personal-demo-v1`，issuer/subject/audience 精确匹配。 |
| I-02 | 目标使用 mTLS-bound OAuth client credentials；共享 secret 仅作最长 30 天迁移例外。 |
| I-03 | 每服务单一 audience 和显式 capability/route allowlist；禁止 `admin`、组和浏览器能力。 |
| I-04 | 业务 workload 固定 `DEMO_TENANT_ID`；知识空间必须有显式 workload grant，无默认空间。 |
| I-05 | access token 5 分钟、测试证书 30 天、delegation grant 8 小时、偏差 60 秒、JWKS 缓存 5 分钟。 |
| I-06 | Demo Owner 可创建/轮换/停用；提权和 secret 例外需独立审计、理由及到期时间。 |
| I-07 | 高风险 token 必须绑定 mTLS 证书；`jti` 按 token 寿命去重，重放失败关闭。 |
| I-08 | 每次副作用重新验证委托交集，长任务至少每 5 分钟复验；依赖不可用时暂停。 |
| I-09 | 依次迁移 Parser、Reranker、告警，再迁移 Kafka/MinIO/PostgreSQL；逐调用方最小 ACL。 |
| I-10 | 每 30 天 owner 复核；连续 30 天未用或 owner 缺失即暂停；脱敏证据保留 90 天。 |

## Demo 准入与禁止晋级

| Gate | 条件 | 当前状态 |
| --- | --- | --- |
| PD0 策略 | 本文经仓库所有者批准；51 项值完整 | DemoApproved |
| PD1 实现 | 仅 `dev/demo`，默认关闭，有确定性测试且不依赖真实企业数据 | Ready |
| PD2 本地运行 | Keycloak demo realm、虚构资产和 ignored secrets 已配置；清理可重复 | Blocked |
| PD3 演示验收 | F～J 实现测试通过，报告显式标记 demo/simulation | Blocked |

PD0/PD1 只解锁代码实现和单机测试，永远不能改变企业 D0～D7。后续运行配置必须同时
验证 `ENVIRONMENT=dev` 与 `IDENTITY_POLICY_PROFILE=personal-demo-v1`；现有环境枚举
不新增 `demo`。`staging`/`production` 遇到 `DemoApproved` 必须拒绝启动。演示结果
不得写入企业 acceptance manifest。

## 首个实现切片：P2.5-F1 会话核心

首个代码 PR 使用 TDD，只建立 provider-neutral 会话核心，不接管现有登录流：

1. 新增 PostgreSQL migration，记录不可猜测 session ID 的摘要、user/tenant、认证方法、
   保证级别、认证/活动/绝对到期、generation、撤销信息和 policy revision；不保存 IdP
   token、密码或原始 subject。
2. 新增 `internal/session` 深层模块，提供 `Establish`、`Authenticate`、`Revoke`，注入
   clock 并返回 allow/deny/reauthenticate；handler 不自行计算超时。
3. 覆盖空闲/绝对到期边界、轮换、重放、逐会话/全会话撤销、并发与存储失败关闭测试。
4. 仅增加 default-off 配置与模块测试，不改变 JWT/Cookie、OIDC、生产行为或部署。

## 第二个实现切片：P2.5-F2 会话凭据验证

F2 在既有 HTTP `Authenticator` seam 上接入预先建立的会话凭据：

1. 仅 `Bearer ps1_<opaque-token>` 进入 session adapter；其他 bearer 继续走 JWT。
2. `ps1_` 路径的无效、到期、撤销或存储失败均失败关闭，不得回退 JWT。
3. session 只定位内部 user/tenant；active、tenant、role 与 scopes 每次从当前用户记录解析。
4. 仅 dev/demo/profile 三重门禁显式启用；默认关闭。F2 不签发凭据，也不修改登录、
   Cookie、OIDC、logout 或 production。
5. 本切片统一使用 standard 动作 `platform.request`；路由风险分类、认证保证输入和
   reauthentication 事务属于 F3。

## 第三个实现切片：P2.5-F3a 风险判断契约

F3a 登记身份绑定、用户/角色/密码、tenant 创建、文档删除/发布、Agent 批准和
generation rollback 为 high-risk action。有效 `ps1_` 会话在 `demo-mfa` 认证时间达到
10 分钟时返回 `401 reauthentication_required`；普通请求不受 freshness 限制。返回挑战
前仍校验当前用户 active 与 tenant，已撤权用户不得获得继续认证的提示。

该切片不建立真正的重新认证事务，也不解析 Keycloak `acr`/`amr`/`auth_time` 或轮换
credential；迁移期 JWT 继续兼容。F3b 再接入提供方证据、单次事务和安全返回路径。

后续 PR 再依次接入凭据签发与 Cookie、退出和 demo 迁移。
