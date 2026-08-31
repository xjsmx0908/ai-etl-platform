# 企业身份提供方适配与 Staging 验收设计

## 状态与范围

提议评审（P2.5-J）。本文把 P2.5-A～I 收敛为选定提供方适配、权威身份对账、隔离
staging 验收和证据晋级契约。当前没有企业 IdP/租户、责任人批准、真实凭据或私有
证据存储输入，因此本阶段不选择提供方、不连接外部租户、不实现仍缺失的 F～I 模块，
也不启用或部署生产身份。

P2.5-J 不是一次大爆炸式重做。A～D 已实现的 Principal、外部绑定、OIDC 和 SCIM
Users 保持权威；E～I 的设计按独立切片实现和验收。只有全部强制门禁通过并由指定
责任人签署后，才可另提 production enablement PR。文档批准本身不是生产批准。

## 当前成熟度与阻塞项

| 切片 | 当前状态 | P2.5-J 进入条件 |
| --- | --- | --- |
| A Principal/生产身份策略 | 已实现并本地验证 | 用选定 provider 复验生产 profile 拒绝 local/test/legacy |
| B 外部身份绑定 | 已实现并本地验证 | 批准 connector→tenant 与精确 subject |
| C OIDC Authorization Code | 已实现；Keycloak 仅协议基线 | 选定 provider 通过真实 discovery、PKCE、JWKS、logout/MFA 契约 |
| D SCIM Users 生命周期 | 已实现并本地验证 | 选定 provider/bridge 通过精确 profile 与 create→login 生命周期 |
| E 生产验收设计 | 已批准设计 | 更新证据 manifest 并由责任人签署 |
| F 会话安全 | 仅设计 | 实现注册表、时钟、认证保证、重新认证、logout 与迁移 |
| G 紧急访问 | 仅设计 | 实现独立租约/认证/审计并完成隔离演练 |
| H 企业组授权 | 仅设计 | 实现完整快照、映射、有效权限和撤权 |
| I workload 身份 | 仅设计 | 实现 workload/grant/委托和真实凭据轮换 |
| J 对账与组合验收 | 本文设计 | 实现对账模块和驱动器，运行真实 staging 并封存证据 |

Keycloak 26.3.3 的已有结果只证明当前 OIDC code flow 的标准兼容性，不能把 Keycloak
选为生产 IdP，也不证明其 SCIM、MFA/logout、Groups、workload 或企业运维能力。

## 提供方选择与配置包

首先由企业责任人完成 P2.5-E 决策矩阵。候选必须提供可导出的、secret-free 配置包：

- 产品/SKU/版本、支持与数据驻留、staging tenant/realm 的私有引用；
- 精确 OIDC issuer、discovery/JWKS、client auth、算法、redirect/logout URI；
- OIDC `sub` 与 SCIM Users/Groups 稳定字段的相等证明，不能从 email/domain 推断；
- SCIM Users 和 Groups 的分页、过滤、PATCH、version/ETag、限流及删除语义；
- `acr`/`amr`/`auth_time`、MFA、风险策略、RP logout 和强制重新认证行为；
- workload issuer/audience、sender binding、轮换及基础设施身份能力；
- 各 connector 的固定内部 tenant、默认角色、组语义、SLA、上限和责任人。

三项强制项（OIDC、SCIM profile、稳定 subject equality）任一低于批准阈值即淘汰。
产品缺口只能选择：淘汰候选、使用被独立运营和验收的窄 bridge，或另提兼容性 PR。
不得通过放宽 issuer/subject/audience、接受 provider role/group、推断 tenant 或扩展核心
生命周期接口来“适配”。

配置分为四层：仓库只保留 schema/示例；部署配置引用 secret manager；真实 tenant、
client、证书和 token 只存在批准的 staging 系统；证据包只保存脱敏导出物的 digest 与
私有对象 ID。每个 profile 绑定 provider/tenant、connector、policy revision、平台
commit、image digest 和启用 feature 集合，禁止同一个未版本化开关控制所有能力。

## Provider adapters 与模块 seam

选定提供方不得成为业务授权权威。其差异限于以下 adapter：

- OIDC adapter：现有 `oidcauth` seam，验证 code/PKCE、issuer、单一 audience、RS256/
  批准算法、JWKS、nonce/state，并输出精确 `(issuer, subject)`；
- Users adapter：现有 SCIM HTTP adapter 翻译到 `identitylifecycle.Provisioner.Apply`；
- Directory read adapter：分页读取权威 Users，输出完整、有版本的对账快照；
- Groups adapter：输出 P2.5-H `groupdirectory.ApplySnapshot` 所需完整快照；
- Assurance/logout adapter：把精确 provider 语义转换为 P2.5-F 内部认证证据；
- Workload credential adapter：输出 P2.5-I `workloadidentity.Authenticate` 所需证明。

只有真实差异才建立 adapter；调用者不看 provider payload。tenant、内部 role、知识空间
映射、会话策略、紧急能力和 workload grant 始终由内部模块决定。

## 权威身份对账模块

新增 `identityreconciliation` 深层模块，接口保持为：

- `Run(ReconcileCommand) -> ReconcileResult`

`ReconcileCommand` 只包含 connector、触发来源、幂等键、批准策略修订和 dry-run/apply
模式；模块内部负责取完整 provider 快照、读取同 connector 的内部生命周期资源、计算
双向漂移、应用批准动作、审计、完成证据和指标。调度器、管理路由和 staging 驱动器不
直接读写 users/bindings/resources。

### 完整快照和并发

Directory adapter 必须使用 provider snapshot token/watermark；若不支持，则首尾读取
源版本/计数摘要，变化时丢弃整轮并从第一页重试。分页缺页、overage、限流耗尽、版本
倒退、重复/冲突 ID、超限或未知完整性均记录为失败尝试，不能产生权威空集或修复。

每个 connector 只有一个有效 run。持久 lease 与单调 fencing token 防止旧 worker
提交；续租失败立即停止副作用。候选 snapshot、plan 和 source watermark 先持久化，
apply 前重新验证 fencing、connector/policy revision 和计划摘要。相同幂等键/摘要返回
原结果；相同键不同摘要拒绝。

### 漂移与修复策略

对账至少区分：

- provider inactive/absent、内部 active：安全撤权候选；
- provider active、内部 absent：provision 候选，仅在批准策略允许时创建；
- subject/resource/connector/tenant 冲突：隔离并人工处理，绝不重绑；
- profile 字段差异：只更新批准字段，不改变 tenant、role、binding 或人工授权；
- 内部已停用但 provider active：默认不自动重激活，必须满足同一历史 identity tuple 和
  批准策略，否则人工处理；
- tombstone/identifier reuse、分页异常或一对多：失败关闭并告警。

默认先 dry-run。apply 只能通过现有 `Provisioner.Apply` 执行允许的 create/update/
deactivate/delete，沿用事务审计、幂等和 tombstone，不允许对账 SQL 直接修表。撤权
优先于增权且不可因新增审批延迟；自动创建/重激活、冲突修复和任何权限扩大保持
`Pending`。单项失败不伪造成功；是否继续处理安全独立项由批准策略决定。

### 完成、失败与新鲜度

每次尝试持久化 started/finished、snapshot watermark/digest/count、plan digest、发现/
应用/隔离计数、结果和有界错误原因。只有完整快照、计划完成且所有强制动作进入确定
终态才推进 `last_completed_at`；失败只推进 `last_attempted_at` 和 `last_failed_at`。

独立指标至少包含 completed timestamp、failed timestamp、snapshot age、duration、
drift/action/quarantine 数量、lease/fencing 冲突和连续失败。现有
`ai_etl_scim_last_success_unixtime` 只表示单次 SCIM mutation，不能复用为对账完成信号。
超过批准新鲜度或错过计划窗口告警；是否让 OIDC 登录/现有会话失败关闭由批准的风险
策略决定，但 provider 已送达的停用必须在下一请求生效，不能等待下次全量对账。

## Staging 环境与执行驱动器

验收使用隔离 staging tenant、非特权测试身份/组/workload、专用 connector 和独立
凭据。staging 不连接生产 tenant，不复制真实人员或组，不使用生产密钥。网络只允许
已批准 issuer/JWKS/SCIM/secret/evidence endpoints；所有外发和管理员入口可审计。

新增 `scripts/enterprise-identity-acceptance.py` 形式的驱动器时，保持它是公开接口的
观察者和证据收集器，而非修复工具：

- 预检 manifest schema、批准项、环境隔离、commit/image/policy revision 和 secret
  引用；真实 secret 仅从环境/secret file 读取且不输出；
- 通过 provider 管理面制造批准的测试状态，通过平台 OIDC/SCIM/业务接口观察结果；
- 对账只调用 `Run` 管理 seam，不能直接更新数据库；数据库查询只作脱敏诊断；
- 每步 fail/pass/blocked、时间、correlation ID 和 artifact digest 原子写入临时私有
  工作区；失败时停止晋级，但尽最大努力撤销测试身份/凭据并记录清理结果；
- 默认 dry-run，真实变更要求显式 apply、唯一 acceptance ID 和已批准私有输出位置。

驱动器本身需要单元测试：schema、redaction、resume、重复运行、partial artifact、digest
篡改、错误环境和 cleanup 失败。仓库不提交真实运行输出。

## 组合验收矩阵

必须按顺序执行并保留证据：

1. **配置与协议：** 精确 issuer/audience/algorithm、discovery/JWKS、PKCE、nonce/state、
   key rotation、错误 issuer/kid/redirect、provider outage。
2. **Users 生命周期：** create→OIDC login、更新、并发重放、停用后下一请求撤权、
   受限重激活、delete/tombstone/ID reuse、SCIM credential overlap/retire。
3. **权威对账：** provider-disabled/platform-active 与 provider-absent/platform-resource
   漂移、内部额外/缺失、冲突 tuple、完整多页、分页中变化、限流、失败重试、并发 run/
   fencing、missed-run 告警；重复 run 必须幂等。
4. **会话：** 已批准 idle/absolute/freshness、MFA assurance、重新认证、单会话/全会话
   撤销、RP logout、IdP 故障时先本地退出、local/test/legacy 生产拒绝。
5. **组授权：** 完整 Groups 分页、overage/嵌套语义、tenant mapping、直接与组来源
   合并、角色冲突、移组/删组/过期快照后撤权、shadow/canary/回滚。
6. **紧急访问：** 独立认证、多方证明、短租约、最小动作、审计/通知失败关闭、到期/
   撤销和隔离演练；不得成为 IdP 或基础设施故障的通用后门。
7. **workload/委托：** 单一 audience、sender binding、grant 交集、轮换/泄露撤权、
   浏览器 token 拒绝、异步 initiator/executor/delegation 复验及基础设施最小 ACL。
8. **非功能与隐私：** SLA、并发、依赖故障、日志/指标/trace/audit 无 secret/subject/
   成员泄漏、备份恢复、所有 owner 告警与 runbook/rollback 演练。

缺失的 F～I 实现使相应项状态为 `blocked`，不是 `pass` 或可跳过。任何 mandatory 项
skipped/blocked/failed、清理未确认、证据泄漏或回滚未演练都阻止晋级。

## 证据 manifest 与晋级

把 `enterprise-identity-acceptance.template.json` 升级为 2.0，并由
`enterprise-identity-acceptance.schema.json` 约束结构、枚举、摘要和 mandatory 结果。
manifest 增加 profile/feature
revision、A～J 成熟度、reconciliation snapshot/run、session/group/emergency/workload
结果、cleanup、风险接受和失效触发器。公开模板只含空字段；完整证据留在批准的不可变
私有系统，manifest 只引用对象 ID、SHA-256、时间和结果。

Schema 对 `decision=approved` 施加条件门禁：全部 mandatory 结果必须是带时间、digest
和 evidence ID 的 pass，测试身份/组/workload/凭据清理全部确认，回滚、到期、责任人
和最终签名不可为空。JSON Schema 只校验结构；driver 仍须验证 digest 对应对象、签名
密码学有效性、时间顺序、证据未过期和所有结果来自同一 acceptance run。

证据必须绑定同一个 provider profile、staging tenant reference、connector、平台 commit/
image、数据库 migration、配置和策略 revision。identity、security、tenant、privacy/
legal、operations owner 对同一个最终 manifest 签名；签名覆盖 manifest digest，而不是
各自不同副本。任何 provider/SKU/issuer/subject mapping/SCIM profile、关键策略、adapter
代码、平台身份代码、证据 schema 或 image 变化，以及批准期限届满，都会使相关证据失效。

production enablement 必须是新的 PR，引用仍有效的签名 manifest，并只启用其中通过的
feature。它必须包含目标环境预检、渐进 canary、观察/回滚 owner、旧 local/shared
credential 退役和变更窗口；不得由验收驱动器自动修改 production。

## 实现顺序与完成定义

1. 批准并填写 P2.5-E～I 的所有 `Pending` 决策，选定 provider 与责任人。
2. 以独立 PR 实现 F session、G emergency、H groups、I workload；每个默认关闭。
3. 实现 directory read adapter 与 `identityreconciliation.Run`，含真实 PostgreSQL、
   并发 fencing、指标/告警和 deterministic provider adapter 测试。
4. 实现 2.0 manifest validator/driver，并完成 staging 环境安全评审。
5. 对选定 provider 运行完整组合矩阵、清理测试资产、封存证据并取得全部签名。
6. 另提 production enablement PR；本文阶段在该 PR 之前不声称生产完成。

P2.5-J 设计完成的定义是：方案经审查、缺口和外部输入明确、2.0 空模板可验证；真正
staging 完成的定义是所有 mandatory 结果为 pass、无 open critical/high finding、清理与
回滚成功、证据有效且签名齐全。两者不得混为同一状态。

## 参考基线（非批准策略）

- [SCIM Protocol RFC 7644](https://www.rfc-editor.org/rfc/rfc7644)
- [OpenID Connect RP-Initiated Logout 1.0](https://openid.net/specs/openid-connect-rpinitiated-1_0.html)
- [OAuth 2.0 Authorization Server Metadata RFC 8414](https://www.rfc-editor.org/rfc/rfc8414)
