# 企业身份决策登记与实现准入

## 目的与边界

本文是 P2.5-E～I 的统一决策索引，供企业责任人在 PR 中逐行提问，并把最终批准关联到
不可变的私有证据。它不选择生产身份提供方（IdP），不保存姓名、tenant、凭据或恢复
材料，也不因文档 PR 合并而自动批准任何策略。Keycloak 26.3.3 仍只是 OIDC 协议基线。

当前准入状态为 **Blocked**：下表 51 项均为 `Pending`。在必需责任人确认批准值、证据
引用、适用环境、批准时间和到期时间前，不开始 F～I 运行代码实现，不连接真实 IdP，
也不执行 staging 或 production 晋级。

个人演示使用独立的 [Personal Demo Profile](personal-demo-identity-profile.md)：其中的
`DemoApproved` 只允许编写默认关闭的本地实现和测试，不更新本表状态，也不满足任何
企业 gate。运行时和验收证据必须区分两种 profile，禁止把 demo 结果提升为企业批准。

## 状态、证据与变更规则

- `Pending`：尚无可审批方案；可转为 `Proposed`。`Proposed` 已有候选值和风险说明，
  可转为 `Approved` 或 `Rejected`。
- `Approved`：所有必需责任人签署同一决策 revision；新 revision 获批后，旧记录转为
  `Superseded`。`Rejected` 的候选只能以新 revision 重新进入 `Proposed`。
- 不得原地改写 `Approved`、`Rejected` 或 `Superseded` 记录。
- “建议基线”只是评审起点。只有“批准值/证据”中的私有不可变对象 ID 和摘要有效，
  状态才可改为 `Approved`；GitHub 文档审批本身不代表企业策略审批。
- 私有记录至少包含决策 ID/revision、批准值、适用 provider/环境、责任角色、签署时间、
  到期时间、风险接受和 detached signature。仓库只保留脱敏引用与 SHA-256。
- provider/profile、subject mapping、SCIM/group profile、关键策略、身份相关代码、证据
  schema、平台 image 或批准期限变化时，受影响批准失效并回到 `Pending`。

## P2.5-E 提供方与生命周期

| ID | 决策 | 建议基线 | 必需责任人 | 状态 | 批准值/证据 |
| --- | --- | --- | --- | --- | --- |
| E-01 | 生产 IdP 与 connector 管理员 | 从已验证矩阵选择；指定一个负责管理员 | 身份 | Pending | — |
| E-02 | connector 到 tenant 所有权 | 一个 connector 固定一个现有内部 tenant；禁止域名推断 | 租户 + 安全 | Pending | — |
| E-03 | 稳定 subject 属性 | provider 字段精确等于 OIDC `sub`，并作为 SCIM `externalId` | 身份 | Pending | — |
| E-04 | 初始角色 | `readonly`；租户管理员经业务批准后在平台内提权 | 租户 | Pending | — |
| E-05 | 存储的用户资料 | 仅 `userName`、显示名、主邮箱；不存 provider 组/角色 | 隐私 + 租户 | Pending | — |
| E-06 | provisioning 模式 | SCIM-only；保持 JIT 关闭 | 安全 | Pending | — |
| E-07 | 停用 SLA | 建议端到端 5 分钟内提交；平台收到事件后下一请求撤权 | 身份运维 | Pending | — |
| E-08 | 权威对账策略 | 建议至少每 24 小时；漏跑一次告警；只走 lifecycle seam 修复 | 身份运维 | Pending | — |
| E-09 | tombstone 保留 | 取审计保留与标识符复用风险要求中的较长者；暂不自动清理 | 法务 + 安全 | Pending | — |
| E-10 | connector 凭据轮换 | 建议最长 90 天、旧新重叠 24 小时后显式退役 | 安全运维 | Pending | — |

## P2.5-F 会话安全

| ID | 决策 | 建议基线 | 必需责任人 | 状态 | 批准值/证据 |
| --- | --- | --- | --- | --- | --- |
| F-01 | 每个 IdP 的 MFA 保证与因子组合 | 仅接受经 provider-specific adapter 明确映射且抗降级的保证 | 安全 + 身份 | Pending | — |
| F-02 | 普通会话空闲超时 | 依据用户风险与可用性测试确定；不能被活动写入合并延长 | 安全 + 产品 | Pending | — |
| F-03 | 普通会话绝对寿命 | 独立于空闲、IdP SSO 和 Cookie 寿命设置硬上限 | 安全 + 产品 | Pending | — |
| F-04 | 高风险操作认证新鲜度 | 按动作风险分级；缺少可信 `auth_time` 时要求重新认证 | 安全 | Pending | — |
| F-05 | IdP 会话/SSO 寿命与续期 | 单独记录 provider 行为并验证强制新登录边界 | 身份 + 安全 | Pending | — |
| F-06 | Cookie 寿命与续期 | 不超过平台会话有效期；轮换后旧凭据立即拒绝 | 安全 | Pending | — |
| F-07 | 并发会话/设备上限 | 选择明确上限及超限处置，支持逐会话撤销 | 安全 + 支持 | Pending | — |
| F-08 | 用户自助查看/撤销会话 | 仅展示脱敏设备元数据；撤销在下一请求生效 | 产品 + 隐私 | Pending | — |
| F-09 | RP/back-channel logout | 本地原子撤销优先；provider logout 失败不恢复本地会话 | 身份 + 安全 | Pending | — |
| F-10 | 双认证迁移批次与切换 | 小批 canary、明确观察期和经验证回滚目标 | 身份 + 运维 | Pending | — |

## P2.5-G 紧急访问

| ID | 决策 | 建议基线 | 必需责任人 | 状态 | 批准值/证据 |
| --- | --- | --- | --- | --- | --- |
| G-01 | 可触发事故类别与等级 | 仅主身份路径不可用等预先枚举的严重事故 | 安全 + 身份 + 业务连续性 | Pending | — |
| G-02 | 独立恢复路径与保管位置 | 至少两个与主 IdP 故障域独立的受控路径 | 安全 + 业务连续性 | Pending | — |
| G-03 | 操作人、批准 quorum 与职责分离 | 禁止自批；操作、批准和复盘角色分离 | 安全 + 合规 | Pending | — |
| G-04 | 抗钓鱼认证/阈值签名与轮换 | 使用可证明持有、可撤销且定期轮换的机制 | 安全架构 | Pending | — |
| G-05 | 最大租约、空闲到期与延期 | 短时硬上限；延期需重新审批且次数有界 | 安全 + 运维 | Pending | — |
| G-06 | 恢复动作与 tenant 范围 | 动作白名单；不授予 `admin`、内容读取或任意 tenant 能力 | 身份 + 平台 + 租户 | Pending | — |
| G-07 | 管理工作站、网络与备用通信 | 独立受控入口，记录来源并拒绝普通终端 | 安全运维 | Pending | — |
| G-08 | 告警通道与升级时限 | 激活即多通道通知；发送失败由持久 outbox 升级 | SOC + 安全 | Pending | — |
| G-09 | 演练频率与成功标准 | 定期 canary；证明启用、最小动作、到期、撤销与恢复 | 业务连续性 + 审计 | Pending | — |
| G-10 | 证据保留、复盘时限与签署 | 独立复盘并绑定完整动作日志和签名证据 | 合规 + 法务 + 安全 | Pending | — |
| G-11 | 基础设施灾难恢复责任边界 | 应用紧急访问不得替代 PostgreSQL/控制面 DR 手册 | SRE + 安全 | Pending | — |

## P2.5-H 组到知识空间授权

| ID | 决策 | 建议基线 | 必需责任人 | 状态 | 批准值/证据 |
| --- | --- | --- | --- | --- | --- |
| H-01 | 组权威来源与稳定 ID | 每个 connector 绑定一个权威目录和不可复用稳定 ID | 身份 + 安全 | Pending | — |
| H-02 | Groups/目录协议范围 | 采用最小可验证 profile；provider 差异封装在 adapter | 身份工程 | Pending | — |
| H-03 | 直接、嵌套和动态组语义 | 默认只接受直接成员；嵌套/动态需显式证明 | 身份 + 安全 | Pending | — |
| H-04 | 同步频率、陈旧窗口与撤权 SLA | 撤权优先；超出批准陈旧窗口时组派生授权失败关闭 | 安全 + 身份运维 | Pending | — |
| H-05 | 映射、组大小、分页和嵌套上限 | 明确硬上限；partial/overage 不得解释为空组 | 平台 + 身份运维 | Pending | — |
| H-06 | admin/manager 管理审批矩阵 | provider 组不得产生平台 admin；manager 映射需租户审批 | 租户 + 安全 | Pending | — |
| H-07 | contributor/manager 提权审批 | reader 可先行 canary；写入/管理能力要求更高审批 | 安全 + 合规 | Pending | — |
| H-08 | 组 tombstone 与审计保留 | 防止 ID 复用，并按隐私最小化要求保存摘要证据 | 法务 + 隐私 + 安全 | Pending | — |
| H-09 | 对账、异常 owner 与告警升级 | 完整快照对账；冲突隔离并指定处置 owner | 身份运维 + SOC | Pending | — |
| H-10 | 授权缓存 TTL 与失效目标 | allow 命中先同步校验 revision fence；缓存仅作加速 | 平台 + 安全 | Pending | — |

## P2.5-I 服务与工作负载身份

| ID | 决策 | 建议基线 | 必需责任人 | 状态 | 批准值/证据 |
| --- | --- | --- | --- | --- | --- |
| I-01 | workload 信任域与授权服务器 | 按环境隔离 trust domain；精确 issuer/subject/audience | 身份 + 平台安全 | Pending | — |
| I-02 | 凭据机制与 secret 例外顺序 | 优先 mTLS/SPIFFE 或 mTLS/DPoP；静态 secret 仅限期迁移 | 安全架构 | Pending | — |
| I-03 | audience、能力词表与机器路由 | 单一 audience、窄能力、显式路由；禁止继承 `admin` | 产品 + 平台 + 安全 | Pending | — |
| I-04 | tenant/platform scope 与空间 grant | 业务 workload 固定 tenant；空间访问需独立显式 grant | 租户 + 安全 | Pending | — |
| I-05 | token/证书/grant TTL、偏差与缓存 | 短期凭据；缓存命中同步校验 credential/grant revision | 身份运维 + SRE | Pending | — |
| I-06 | 生命周期与例外审批矩阵 | 创建、提权、轮换、停用分权；例外有 owner 和到期日 | 安全 + 合规 | Pending | — |
| I-07 | `jti` 重放与 sender binding | 高风险必须 sender-constrained；bearer 例外启用重放防护 | 安全架构 | Pending | — |
| I-08 | 委托撤权、长任务复验与失败处理 | 每次副作用验证委托交集；长任务按批准频率复验 | 平台 + 安全 + SRE | Pending | — |
| I-09 | 基础设施账号/ACL/TLS 迁移 | 按 topic/bucket/schema/index 建最小身份并逐链路 canary | SRE + 资源责任人 | Pending | — |
| I-10 | owner 复核、未使用身份与证据保留 | 定期复核；owner 缺失、到期或长期未用自动暂停 | IAM + 合规 + 隐私 | Pending | — |

## 实现准入矩阵

| Gate | 必须满足 | 解锁内容 | 当前状态 |
| --- | --- | --- | --- |
| D0 决策完整 | E-01～I-10 全部 `Approved` 且未过期 | 建立实现分支与最终接口配置 | Blocked |
| D1 会话 | D0 已通过；F 设计仍有效 | P2.5-F 持久会话、保证、重认证和退出切片 | Blocked |
| D2 紧急访问 | D0 已通过；G 设计仍有效 | P2.5-G 租约/审计/outbox 与最小恢复动作 | Blocked |
| D3 组授权 | D0 已通过；H 设计仍有效 | P2.5-H 目录、映射、revision fence 与撤权 | Blocked |
| D4 workload | D0 已通过；I 设计仍有效 | P2.5-I registry、grant、凭据和委托 | Blocked |
| D5 对账/driver | D0、D1～D4 已通过且实现验收有效 | P2.5-J adapter、对账、validator 和 staging driver | Blocked |
| D6 staging | D1～D5 通过，隔离 tenant/凭据/owner 可用 | 真实组合矩阵、清理、证据和签名 | Blocked |
| D7 production | 有效 approved manifest、无高风险 finding、独立启用 PR | canary production enablement | Blocked |

安全不变量不因 gate 阻塞而放宽：功能默认关闭；tenant/角色/能力保持平台权威；未知、
陈旧或不完整输入失败关闭；禁止真实 secret 进入仓库；任何 production enablement 必须
另提 PR。批准完成后，应先从 P2.5-F 的数据库结构与 provider-neutral `session` seam
开始 TDD，实现与部署仍分别评审。

## 审批操作步骤

1. 在本文件对应 ID 行发起 review comment，责任人提出候选值、风险和所需证据。
2. 在私有审批系统创建不可变记录；仓库 PR 仅加入对象 ID、SHA-256 和有效期。
3. 所有必需责任人核验同一 revision 后，将该行改为 `Approved`；部分签署仍为
   `Proposed`。后续独立实现的 validator 必须检查重复 ID、空 owner/evidence、过期和
   gate 依赖，并在接入 CI 前保持自动准入关闭；当前文档切片尚未实现该 validator。
4. 安全、身份、租户、隐私/法务、运维和业务责任人共同确认 D0；随后另提首个实现 PR。

详细约束分别见[生产身份验收](enterprise-identity-production-acceptance.md)、
[会话安全](enterprise-session-security-design.md)、[紧急访问](enterprise-emergency-access-design.md)、
[组授权](enterprise-group-authorization-design.md)、[工作负载身份](enterprise-workload-identity-design.md)
和[提供方与 staging 验收](enterprise-identity-provider-staging-design.md)。
