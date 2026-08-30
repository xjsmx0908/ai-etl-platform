# 企业服务与工作负载身份设计

## 状态与范围

提议评审（P2.5-I）。本文定义非人类调用方的身份、能力、委托、轮换和审计契约；
不选择真实授权服务器、SPIFFE 实现或云 IAM，不签发生产凭据，不修改运行时代码，
也不启用生产机器访问。协议、令牌寿命、审批、轮换和缓存数值均为 `Pending`。

本设计覆盖调用平台业务接口的自动化客户端、平台内部 HTTP 工作负载和异步执行器。
数据库、Kafka、Redis、对象/搜索存储凭据属于基础设施访问；LLM、embedding 等 API
key 属于外部资源凭据。它们需要同一资产清单和轮换纪律，但不能成为平台
`Principal`，也不能拿来调用用户接口。

## 已核实的当前状态

- `auth.Principal` 已预留 `AuthenticationMethodService`，但没有服务身份认证器、注册
  表、签发流程或服务授权策略；`SubjectID` 仍被多数调用方当作内部用户 ID。
- 平台 JWT 是面向用户的 HS256 token，能力由 `admin/user/readonly` 角色映射；它没有
  固定 issuer/audience，也不能安全地复用为机器凭据。
- Parser 使用一个共享 `X-Internal-Token`；告警 relay 使用共享 Bearer token；Reranker
  客户端可发送 key，但服务端当前没有对应验证。共享 secret 不能区分具体调用者、
  租户、能力或单独撤权。
- ETL worker 通过 Kafka 处理用户提交的任务，Agent 编排器代表用户执行工具；当前
  运行记录包含租户/用户上下文，但没有统一的“发起者 + 执行工作负载 + 委托”模型。
- PostgreSQL、Kafka、Redis、MinIO、Qdrant/ES 及模型供应方各自使用连接 secret 或
  API key；部分本地基础设施仍无传输身份，不能视为企业生产认证。

因此，不能通过创建一个普通 `users` 行、复制 admin JWT、共享 parser token 或把云
角色名映射为平台 scope 来实现企业服务身份。

## 身份类别与信任边界

| 类别 | 示例 | 平台身份语义 |
| --- | --- | --- |
| 人类身份 | 浏览器 OIDC 用户 | 绑定内部 user，可使用会话、组和人工审批 |
| 业务工作负载 | CI 导入器、租户自动化客户端 | 独立 workload，固定租户和明确业务能力 |
| 平台工作负载 | Query API、ETL worker、Parser、Reranker、Agent executor | 独立 workload，固定环境、audience 和最小内部能力 |
| 委托执行 | 用户上传后的 worker、用户启动的 Agent 工具 | 同时保留人类发起者、执行 workload 和有界委托 |
| 基础设施/外部资源凭据 | PostgreSQL、Kafka、MinIO、LLM API key | 仅访问指定资源，不转换为平台 Principal |

开发、测试、staging、production 使用不同信任域和身份，凭据不能跨环境接受。一个
workload 不能同时表示多项部署职责；同一镜像的 worker 与 Query API 也必须是不同
身份。

## 安全不变量

1. **机器不是用户。** `Principal` 必须显式携带 `Kind=human|workload`。workload 使用
   独立内部 ID 和注册表，不创建虚假 user，不拥有密码、浏览器 cookie、MFA 会话、
   企业组或紧急访问资格。
2. **内部注册表决定权限。** 只有精确的 `(trust_domain, issuer, external_subject)`
   绑定可解析到一个活跃 workload。token 的 tenant、role、group、email、云标签或
   自报 scope 不能创建身份或扩大权限。
3. **租户先固定。** 业务 workload 注册时绑定一个内部租户；平台 workload 只能使用
   明确的 platform scope。任何请求体、队列消息、hostname 或凭据声明都不能选择或
   切换租户。
4. **audience 精确匹配。** 每个接收模块只接受自己的单一 audience、批准的 issuer、
   算法和凭据类型。供 Parser 使用的证明不能调用 Query API，浏览器 token 不能调用
   内部 workload 接口，多个 audience 默认拒绝。
5. **能力取交集。** 有效能力是 token 所请求能力、内部注册 grant、目标 audience、
   环境策略和资源策略的交集；任何来源都只能缩小，不能扩大。workload 永不获得平台
   `admin`、用户/租户管理、组映射、紧急访问或人工审批能力。
6. **每次请求检查当前状态。** allow 结果绑定 workload、grant 和 credential 修订。
   命中缓存前同步检查当前撤权栅栏；outbox 只加速失效。停用、grant 删除或凭据吊销
   后下一请求失败关闭。
7. **短期、可绑定、可轮换。** 优先使用运行时环境签发的短期、sender-constrained
   证明（mTLS/SPIFFE SVID 或 mTLS/private-key 绑定 OAuth token），不把静态 bearer
   secret 烘焙到镜像、仓库、队列或日志。静态 secret 只作为有期限迁移例外。
8. **资源授权仍归资源模块。** 平台认证只证明 workload 和粗粒度能力；知识空间、
   文档、Agent 工具及基础设施权限仍由各自模块按当前状态判定。

## 深层模块与接口

新增 `workloadidentity` 深层模块，在现有 `auth.Authenticator` 接缝后隐藏协议验证、
精确绑定、生命周期、grant 交集、撤权修订和审计。对业务调用者保持一个主要接口：

- `Authenticate(RequestEvidence) -> auth.Principal`

管理面使用独立的 `Apply(WorkloadCommand) -> WorkloadResult` 和只读 `List`；写入在一个
PostgreSQL 事务中验证操作者、环境、租户、audience、能力白名单、所有者、到期时间、
幂等摘要及审批，并原子提交状态和成功审计。提供方 JWT、mTLS/SPIFFE 或本地测试适配器
是模块内部 seam，不把协议字段泄漏给路由。

`auth.Principal` 后续需增加 kind、内部 workload/user subject、tenant/platform scope、
认证强度、能力及身份/grant 修订。人类专用路由必须显式要求 `Kind=human`；接受机器
访问的路由必须显式要求 `Kind=workload` 和细粒度能力，不能仅因字符串 `query`、
`upload` 或 `admin` 碰巧相同而放行。

## 注册表、grant 与生命周期

workload 记录至少包含内部 ID、名称、用途、责任人、环境、tenant/platform scope、
状态、允许 audience、创建/到期时间和修订。外部绑定保存 trust domain、issuer、稳定
subject、凭据类型及公钥/证书/云身份引用；只存不可逆指纹和 secret-manager 引用，不
保存私钥或可用 bearer token。

grant 单独保存 workload、audience、能力、可选知识空间/资源约束、审批证据、有效期和
修订。状态为 proposed/active/suspended/retired；到期、失去责任人、连续未使用或环境
退役均触发复核或失败关闭。删除使用 tombstone，防止外部 subject 或 client ID 重用
接管历史权限。

能力采用业务动作而非人类角色，例如 `knowledge.query`、`ingestion.submit`、
`agent.execute-approved`、`parser.parse`。最终词表和哪些路由允许机器访问保持
`Pending`。管理类自动化若确有需要，必须另建窄能力并单独审批，不能授予 `admin`。

知识空间对机器使用独立 `service_space_grants` 来源，不写入
`knowledge_space_members`。`knowledgecatalog` 合并时保留人类直接、组派生和 workload
来源的类型与修订；workload 没有默认空间，必须显式指定批准的空间和动作。

## 凭据签发、验证与轮换

推荐顺序为：同集群工作负载使用经批准信任域的 SPIFFE/mTLS；外部业务自动化使用企业
授权服务器的 Client Credentials，并优先采用 mTLS 或 `private_key_jwt`；无法支持的
系统才在有期限例外下使用 secret。是否采用 SPIFFE、授权服务器、算法、token TTL、
证书 TTL、时钟偏差、JWKS 缓存及例外期限均为 `Pending`。

接收方验证 TLS、issuer、单一 audience、subject、时间、算法、key usage 和 sender
binding，再由内部注册表解析。JWT access token 应采用明确 profile；不得接受 ID token、
浏览器 session、刷新 token 或 unsigned token。高风险写入还需幂等键、请求摘要；若
使用可重放 bearer，需批准的 `jti` 重放防护。token 验证成功但注册表/撤权栅栏不可用
时拒绝，不回退到 token scope。

轮换采用新旧公钥/证书短暂重叠：先登记新证明，验证 canary，再切换签发方，观察旧
证明不再使用后撤销旧指纹。紧急泄露立即 suspend workload 或 credential 修订，不等待
token 自然过期；记录影响范围、最后使用、轮换所有者和完成证据。

## 委托与异步执行

用户触发的 Kafka/Agent 操作不能把浏览器 token 放入消息，也不能只依赖 worker 自身
权限。接收请求时创建不可变、有期限的 `DelegationGrant`，绑定发起人、执行 workload、
租户、资源/知识空间、动作、策略修订、请求摘要、最大使用次数和到期时间；消息只携带
内部 grant ID、任务 ID 和防篡改关联信息。

执行器每次产生敏感副作用前验证自己的 workload 身份和 grant 当前状态。用户停用、
空间撤权、任务取消、审批撤回或 grant 到期后失败关闭。长任务在检查点重新验证，不能
因为最初已入队就永久保留权限。

Agent 高风险工具记录 `initiator_human_id`、`executor_workload_id` 和 grant；审批必须
绑定确切工具/参数摘要、策略和期限。workload 不能批准自己的请求，工具也不能用执行器
身份横向调用未在 grant 中列出的租户或资源。

## 内部与外部依赖迁移

- Parser、Reranker、告警 relay：先加入独立 audience 和 workload 验证，再以双验证
  canary 迁移；共享静态 token 只在有期限迁移窗口保留，不能作为长期回退。
- Kafka、PostgreSQL、Redis、MinIO、Qdrant/ES：使用每 workload 独立账号、ACL 和
  TLS，或经批准的云工作负载身份；Query API 与 worker 不共享超级凭据。
- LLM、embedding、通知供应方：独立最小权限 key/云身份只供出站适配器使用，按供应方
  与环境分离；供应方凭据不能映射为内部 Principal。
- 健康检查只证明存活，不授予业务访问；metrics/admin/debug 接口使用独立 audience
  和网络策略，不与数据面凭据共用。

## 审计、可观测与隐私

认证和管理审计记录内部 workload、actor kind、tenant/platform scope、audience、能力、
credential/grant 修订、结果、原因码、相关 ID 和时间，不记录 token、私钥、完整证书、
外部 subject 原文或请求敏感内容。指标至少覆盖成功/拒绝、错误原因、签发/验证延迟、
凭据/owner 到期、最后使用年龄、撤权栅栏失败和共享 secret 剩余数量。

必须能回答“哪个人通过哪个 workload，以哪项 grant，在何时对哪个租户资源做了什么”，
同时区分纯平台维护任务。审计不可用时，高风险签发、grant 变更和委托执行失败关闭。

## 待批准的企业决策

| 决策 | 必需责任人 | 批准值/状态 |
| --- | --- | --- |
| 内部/外部 workload 信任域与授权服务器 | 身份 + 平台安全 | Pending |
| mTLS/SPIFFE、private_key_jwt 与 secret 例外顺序 | 安全架构 | Pending |
| audience、能力词表及允许机器访问的路由 | 产品 + 平台 + 安全 | Pending |
| workload tenant/platform scope 与知识空间 grant 模型 | 租户责任人 + 安全 | Pending |
| token/证书/grant TTL、时钟偏差和缓存 | 身份运维 + SRE | Pending |
| 创建、提权、轮换、停用和例外审批矩阵 | 安全 + 合规 | Pending |
| `jti` 重放防护和高风险 sender binding | 安全架构 | Pending |
| 委托撤权、长任务复验频率和失败处理 | 平台 + 安全 + SRE | Pending |
| 基础设施账号/ACL/TLS 迁移计划 | SRE + 各资源责任人 | Pending |
| 所有者复核、未使用身份和证据保留期限 | IAM + 合规 + 隐私 | Pending |

任何空白决策都会阻止 P2.5-J 的真实凭据验收和生产机器访问。

## 迁移、回滚与验收

1. 建立只读资产清单，定位每个共享 secret、调用方向、环境、责任人和最小能力；不签发
   新权限。
2. 注册独立 workload 和 shadow grant，记录旧调用与新策略的 allow/deny 差异。
3. 从 Parser/Reranker 等单一 audience 的低风险链路 canary，随后迁移业务自动化、
   异步委托和基础设施账号；每批次轮换并证明旧凭据拒绝。
4. 回滚只停用新验证路径并恢复该调用方明确记录、仍有效的旧凭据；不得恢复已泄露、
   已过期或被其他变更替代的 secret，也不得放宽 audience/tenant/capability。

验收至少覆盖：issuer/subject/audience/algorithm 混淆，多 audience，ID/browser token
注入，跨租户/跨环境/跨知识空间，伪造 scope/role/group，未知/停用/过期 workload，
grant 删除与下一请求撤权，注册表/issuer/JWKS/时钟/网络故障，密钥轮换与紧急吊销，
共享 secret 退役，重放与并发幂等，以及审计无凭据泄漏。

委托验收还必须覆盖用户入队后停用、空间撤权、任务取消、长任务复验、审批过期/撤回、
执行器更换和 workload 自审批；基础设施验收证明每个身份只能访问指定 topic、bucket、
database/schema/index。P2.5-J 必须绑定选定提供方/信任域、真实凭据、平台 commit/image、
策略修订、轮换与回滚证据，任何相关变更都会使证据失效。

## 实现切片

后续实现拆为：Principal kind 与 workload 注册表；协议验证适配器；细粒度 grant 与路由
门禁；知识空间 workload grant；委托 grant 与异步复验；内部 HTTP 身份迁移；基础设施
ACL/TLS；最后是轮换、对账、告警和真实环境验收。每个切片默认关闭并保留确定性测试。

## 参考基线（非批准策略）

- [OAuth 2.0 Mutual-TLS Client Authentication（RFC 8705）](https://www.rfc-editor.org/rfc/rfc8705)
- [JWT Profile for OAuth 2.0 Access Tokens（RFC 9068）](https://www.rfc-editor.org/rfc/rfc9068)
- [OAuth 2.0 Security Best Current Practice（RFC 9700）](https://www.rfc-editor.org/rfc/rfc9700)
- [SPIFFE Overview](https://spiffe.io/docs/latest/spiffe-about/overview/)
