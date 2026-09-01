# Technical Design Document

## Approved P0 Scope

The implementation proceeds in vertical test-driven slices through four seams:
the knowledge catalog interface, SQL migration runner, Query API HTTP handlers,
and the isolated full-stack RAG flow.

## Persistence

Migration `0005_knowledge_spaces.up.sql` creates tenant-scoped spaces and
memberships. Migration `0006_tenant_default_knowledge_space.up.sql` repairs any
tenant missing its default space and installs an `AFTER INSERT` tenant trigger,
so user membership creation can always reference `user-uploads`. Space roles
are `reader`, `contributor`, and `manager`; tenant admins bypass membership
within their tenant. Documents gain a required space
foreign key and `draft|published|retired` publication status.

Backfill rules are deterministic:

- missing or `user-uploads` metadata maps to the default production space;
- other legacy slugs create non-default spaces;
- `enterprise-demo` is typed `demo`;
- completed active production documents become published;
- incomplete, failed, retired, and demo documents remain non-queryable until an
  authorized manager publishes them.

## Interfaces and Routes

The catalog exposes `Resolve`, `AuthorizeUpload`, `List`, and `FilterEvidence`.
Routes add `GET/POST /v1/knowledge-spaces` and accept
`knowledge_space_id` on upload and query. Query responses include the resolved
space. Deprecated free-form scope fields remain response-only during P0.

## Safety and Rollback

The migration is additive and retains legacy metadata. Before production
rollout, export the `documents`, `knowledge_spaces`, and membership tables.
Rollback disables catalog enforcement with a temporary deployment feature flag
and restores the database snapshot; no destructive down migration is used.

## Evidence Sufficiency

After tenant, permission, knowledge-space, publication, and document lifecycle
filters run, queries containing strong business identifiers require at least one
remaining candidate that contains every requested identifier in its document
id, chunk id, content, or configured exact metadata. A mismatch returns the
canonical no-evidence response before generation and exposes no sources or
citations. Response diagnostics report only required/matched booleans; raw
identifiers are not logged or returned as diagnostics.

## Verification

Tests cover default resolution, membership denial, upload authorization,
published-only evidence, catalog outage behavior, migration ordering, handler
status codes, and two same-topic documents in different spaces. Completion also
requires `gofmt`, `go vet`, full Go/Python tests, Web build, Compose validation,
and the deterministic RAG evaluation.

## Exact-candidate Publication

Migration `0017_exact_candidate_publication.up.sql` adds the last successful
publication idempotency key and candidate request hash to `document_releases`.
Assessment reads an authoritative candidate only when the release is resolved
and both backend observations equal the sealed active-manifest identity. The
approval tool requires and persists every candidate field.

Publication acquires a release row lock and a shared manifest lock, checks the
authenticated tenant and administrator role, and rejects any version,
generation, digest, count, health, or revision change. Document publication,
release CAS, and the immutable success audit commit together. An identical
idempotency-key/request-hash replay returns success without a second audit or
revision; a reused key with altered arguments fails closed.

## Published-release query cutover

`publicationrelease.PostgresStore.ResolveVisibility` is the single read-side
authority. It batch-resolves candidate document/version/generation identities
for the authenticated tenant and returns visibility only for an exact published
release whose active manifest is sealed, reconciled, and digest/count-complete
in Qdrant and Elasticsearch. Resolver errors or malformed result lengths fail
closed. The retrieval engine applies this before fusion and cache writes; the
content-search handler applies it before document aggregation.

Durable completion in `user-uploads` calls `PublishAutomatic`, which derives the
healthy active generation in PostgreSQL and is idempotent on replay. Managed
replacement uploads retain the old release and must keep the existing
permission and knowledge space; only a later exact-candidate approval performs
the release cutover.

## Recoverable document deletion

Migration `0018_recoverable_document_deletion.up.sql` adds
`documents.deletion_status` and one tenant/document-unique deletion job with
claim token, lease, retry time, attempts, last error, exact object-key snapshot,
and per-dependency completion timestamps. `Accept` locks the document and makes
job creation, release revocation, queued-ingestion cancellation, and immutable
audit append one transaction. Replays return the existing job.

The ETL worker collector uses `FOR UPDATE SKIP LOCKED` claims and rejects stale
finish tokens. It waits while any ingestion job holds an active processing
lease. Partial completion persists successes and schedules a retry; full
completion appends its audit and deletes the catalog row, whose foreign-key
cascade removes releases, ingestion state, manifests, and the deletion job.
Prometheus exposes only fixed state/condition/outcome labels.

## P2.4 governance acceptance harness

The governance overlay exposes worker metrics on loopback and replaces only its
isolated MinIO data volume with a bounded tmpfs. The Python driver calls public
HTTP for all business observations and uses Compose for Kafka, process, Qdrant,
Elasticsearch, and MinIO interruption. Because restarting the ephemeral MinIO
container clears its bucket, the driver restarts Query API after recovery to
exercise the normal bucket-initialization path before awaiting deletion.

Run `bash scripts/governance-acceptance.sh`; set `GOVERNANCE_REPORT_DIR` to
choose the retained evidence directory. `GOVERNANCE_BUILD=0` may reuse local
images while developing the harness, but release evidence should use the
default clean build. Unit contract tests run under the existing
`python3 -m unittest discover -s scripts/tests -p 'test_*.py' -v` CI step; the
dependency-heavy exercise remains an explicit release command.

## P2.5-A principal and production identity policy

`auth.Authenticator` has one method, `Authenticate(*http.Request) (Principal,
error)`. `auth.Middleware` is adapter-neutral and writes both the complete
principal and compatibility tenant/user/role/scope values into request context.
The HS256 platform-token adapter validates an exact algorithm, expiry, and
signature before applying environment policy.

`IssueToken` marks password-authenticated sessions `local`; test helpers mark
offline credentials `test`; missing method claims are `legacy`. In production,
only known active local subjects are accepted in this slice. PostgreSQL owns
tenant, role, capabilities, and token-version revocation even when signed claims
disagree. Outside production, local, test, and legacy credentials remain
available for migration and deterministic evaluation.

Focused tests exercise the identity interface and protected HTTP middleware,
including algorithm confusion, claim-based privilege injection, unknown and
inactive users, revocation, legacy/test rejection, and non-production
compatibility. No OIDC/SCIM adapter, production federation, or deployment is
part of this slice.

## P2.5-B external identity bindings

Migration `0019_external_identity_bindings.up.sql` gives each normalized
`(issuer, external_subject)` pair one global binding and uses a composite
user/tenant foreign key to make cross-tenant rows unrepresentable. Issuers must
be absolute HTTPS URLs with no userinfo, query, or fragment. Scheme and host are
lowercased; issuer paths and exact case-sensitive subjects are preserved.

`externalidentity.Directory.Resolve` is the future provider adapter's mapping
seam. It joins the binding to the current active user and returns a federated
policy principal whose capabilities are derived from the current internal role.
It never validates a token. `externalidentity.Manager` owns list/create/delete,
tenant-admin checks, normalization, conflicts, and atomic mutation audit.

The authenticated management routes are:

- `GET /v1/users/{user_id}/external-identities`
- `POST /v1/users/{user_id}/external-identities` with `issuer` and `subject`
- `DELETE /v1/users/{user_id}/external-identities/{binding_id}`

All require the existing admin capability. PostgreSQL integration tests cover
global uniqueness, live role/active state, tenant isolation, and rollback when
either create or delete audit fails. A concrete IdP, OIDC/JWKS, SCIM/JIT, group
mapping, production enablement, and deployment remain outside this slice.

## P2.5-C OIDC authorization-code adapter

`internal/oidcauth.Authenticator.Authenticate(ctx, CodeExchange)` is the deep
provider seam. Construction loads HTTPS discovery and an allowed RSA signing
set. Authentication exchanges a code with its PKCE verifier, accepts only
RS256, and requires exact issuer, client audience, expiry, issued-at, nonce,
subject, and key id. An unknown key id or cached-key signature failure triggers
at most one JWKS refresh before full validation, covering both new- and
same-`kid` rotation. Provider and directory failures return one fail-closed
error without exposing upstream bodies or identifiers.

`Flow` generates independent 256-bit state, nonce, and verifier values and an
S256 challenge. Its transaction store exposes only save and atomic consume.
Development uses memory; non-development uses Redis TTL plus `GETDEL` for
cross-replica callback safety. Return paths are restricted to same-origin
absolute paths. The callback re-reads the internal user before issuing an
`auth_method=federated` platform session.

The Web BFF sets short-lived HttpOnly state and long-lived HttpOnly session
cookies. It never exposes provider tokens, state, nonce, verifier, or the
platform JWT to client JavaScript. OIDC configuration is default-off. Production
with OIDC enabled rejects ordinary password login and local/test/legacy platform
sessions. Keycloak is the first standards acceptance target; SCIM/JIT, groups,
service identities, break-glass, deployment, and production provider selection
remain outside this slice. Automated protocol coverage uses a real TLS OIDC
test provider. The opt-in `TestKeycloakAuthorizationCodeAcceptance` gate also
passed against Keycloak 26.3.3 over HTTPS; its environment variables keep realm
credentials out of source control and ordinary CI.

## P2.5-D identity lifecycle provisioning

Migration `0020_identity_lifecycle.up.sql` records connector policy, provider
resource ownership, retained tombstones, and idempotent responses. The
`identitylifecycle.Provisioner` commits user creation or mutation, exact
issuer/subject binding, session revocation, lifecycle ownership, success audit,
and replay state in one PostgreSQL transaction. A connector is permanently
bound to one tenant and issuer; only its configured `readonly` or `user` role
may be assigned at create time.

The default-off `/scim/v2/Users` adapter supports exact `userName` lookup, GET,
POST, PUT, bounded PATCH, and DELETE. It authenticates against overlapping
secret-file credentials, rejects role/group and unknown-field injection, and
maps only explicit `externalId` to the exact OIDC subject. DELETE retains the
binding and resource tombstone; reactivation requires the same connector,
resource, issuer, and subject. SCIM-owned users cannot receive a local password
or have their lifecycle/tenant changed through the admin API.

Production stays blocked until a chosen IdP passes the complete SCIM lifecycle
to OIDC login acceptance flow and its ownership, SLA, retention, reconciliation,
and credential-rotation policies are approved.

## P2.5-E production identity acceptance design

The production decision is evidence-driven rather than encoded as another
provider abstraction. Candidate IdPs are compared against the existing OIDC,
external-directory, and lifecycle interfaces. Provider configuration may vary,
but exact issuer/subject identity, connector-owned tenant/default role, atomic
revocation/tombstone behavior, and fail-closed errors cannot vary.

The staging gate observes one composed SCIM-to-OIDC lifecycle through public
interfaces and retains a signed, secret-free bundle tied to provider profile,
platform commit/images, owners, expiry, and rollback evidence. Provider and
policy selection, implementation, and production enablement remain blocked on
separate approval; no new runtime seam or deployment is introduced here.

## P2.5-F 企业会话安全设计

[企业会话安全设计](enterprise-session-security-design.md)规定未来由注册表支持的
平台会话：唯一会话 ID、空闲与绝对到期、认证新鲜度、与提供方无关的认证保证、
凭据轮换，以及逐会话/全会话撤销。拟议的深层会话模块针对策略操作返回 allow、
deny 或 reauthenticate；业务处理器不解析提供方声明，也不实现到期逻辑。

选定的 OIDC 适配器必须把其精确 `acr`、`amr`、`auth_time` 契约转换为经批准的
内部认证证据。高风险操作将新的 state/nonce/PKCE 事务绑定到当前会话，并在
轮换凭据前要求相同内部 subject。退出先在本地撤销并清除 Cookie，再执行可选且
经验证的 RP 发起提供方退出。

实现有意推迟并拆为单独评审的数据库结构、注册表认证、认证保证/重新认证、退出和
批次迁移切片。所有数值寿命、因子映射、设备上限和切换周期保持 `Pending`；
P2.5-G 负责紧急访问，P2.5-J 负责选定提供方适配器和 staging 证据。

## P2.5-G 企业紧急访问设计

[企业紧急访问设计](enterprise-emergency-access-design.md)定义未来独立的
`emergencyaccess` 深层模块。激活需要多方签署且单次使用的事故证明，以及操作人
用独立恢复认证器完成 challenge 持有证明；证明本身不能作为 bearer 凭据。模块
在一个持久事务中创建短期租约、会话、不可变审计和通知 outbox。

紧急 principal 使用独立认证方式和动作型能力，不继承 `admin` scope。逐请求
`Execute` 重新验证租约、操作者、租户、动作、会话、到期和撤销，在副作用前持久化
幂等操作意图和审计，再通过内部受限适配器执行并记录结果；审计或租约存储故障
失败关闭，外部不确定结果由操作日志收敛。绝对/空闲到期、手工关闭和延期代次
轮换都会撤销旧凭据。

实现推迟并拆为租约数据模型、证明/保管适配器、独立认证、最小恢复处理器、通知/
复盘证据及隔离演练切片。所有人数、quorum、时限、认证方式、网络和告警参数均为
`Pending`；本设计不创建真实恢复身份或密钥，也不覆盖基础设施灾难恢复。

## P2.5-H 企业组到知识空间授权设计

[企业组到知识空间授权设计](enterprise-group-authorization-design.md)定义未来
`groupdirectory` 完整快照模块、租户 `groupmapping` 模块及
`knowledgecatalog` 有效权限合并。映射键固定为连接器和稳定 provider group ID，
只能授予同租户空间的 reader/contributor/manager；raw OIDC group claim、显示名、
域名和路径不能授予权限或推导租户/平台角色。

现有 `knowledge_space_members` 保持直接授权，组授权独立持久化。Resolve/List 每次
读取当前直接成员、完整组快照和活跃映射，同一空间取最高允许角色并保留来源修订；
组移除、映射删除、用户/空间/连接器停用或快照过期后下一请求撤权。短期缓存绑定
映射/快照修订；每次命中前同步校验授权修订栅栏，事务 outbox 只作加速失效，状态不
可验证时回源 PostgreSQL。跨页同步必须使用 snapshot token/watermark，或首尾版本
变化即整轮重试，不能把混合版本写成完整快照。

实现拆为组目录模型、真实提供方适配器、映射管理、有效权限、缓存/对账/告警及
shadow/canary 迁移切片。同步、嵌套、审批、撤权 SLA、上限和缓存值均为 `Pending`；
本阶段不实现或启用真实组授权。

## P2.5-I 企业服务与工作负载身份设计

[企业服务与工作负载身份设计](enterprise-workload-identity-design.md)定义未来
`workloadidentity` 深层模块：在一个 Authenticate 接口后隐藏 JWT/mTLS/SPIFFE
验证、独立注册表、精确 issuer/subject/audience、grant 交集、生命周期、撤权修订和
审计。`auth.Principal` 必须区分 human/workload，人类专用路由和机器路由分别门禁；
机器不创建 user、不使用会话/组/紧急访问，也不获得平台 admin。

知识空间使用独立 workload grant；用户触发的 Kafka/Agent 操作持久化有期限的委托
grant，绑定发起人、执行 workload、租户、资源、动作和策略修订，执行敏感副作用与
长任务检查点时重新验证发起者当前权限与执行器 grant 的交集。Parser/Reranker/告警
共享 token、基础设施账号和供应方 key
按调用方与环境拆分；资源凭据不能转换为平台 Principal。

实现拆为 Principal/注册表、协议适配器、细粒度 grant、知识空间授权、异步委托、
内部 HTTP 迁移、基础设施 ACL/TLS 及真实轮换验收。信任域、协议、能力、TTL、审批、
缓存和迁移值均为 `Pending`；本阶段不签发或启用生产机器凭据。

## P2.5-J 提供方适配、权威对账与 Staging 验收设计

[企业身份提供方适配与 Staging 验收设计](enterprise-identity-provider-staging-design.md)
把 provider 差异限制在 OIDC、Users/Groups directory、assurance/logout 和 workload
credential adapters。新增 `identityreconciliation.Run` 深层模块，内部获取一致的
完整 provider 快照、比较 connector-owned 内部资源、生成/应用批准的 lifecycle 动作，
并持久化 lease/fencing、幂等计划、审计及独立完成/失败证据。每项 lifecycle mutation
在同一事务 CAS 校验 run fence、provider profile、policy 和 action digest；无 snapshot
token 时要求两次完整稳定 ID+version canonical digest 相同，count 单独无效。

验收驱动器只调用 provider 管理面和平台公开 seam，不直接修表；2.0 manifest 绑定
provider profile、connector、policy、commit/images、A～J feature 结果、清理/回滚和
八类固定证据失效触发器；签名固定使用 RFC 8785，绑定排除 signature envelope 的同一
canonical payload digest 及逐责任人 detached signatures。风险接受项必须绑定责任人与证据。
已有 Keycloak 只证明 OIDC 基线，A～D 已实现而 F～I
仍为设计；blocked/skipped/failed mandatory 项均阻止 production enablement。

实现顺序是先批准 Pending 输入并分别实现 F～I，再实现对账、driver/validator 并运行
真实隔离 staging。生产启用必须是引用有效签名证据的后续独立 PR，本设计不选择 IdP、
创建凭据、连接外部 tenant 或部署。

## P2.5 企业决策登记与实现准入

[企业身份决策登记与实现准入](enterprise-identity-decision-register.md)汇总 E～I 的
51 项 `Pending` 决策，并以 `E-01`～`I-10` 稳定 ID 连接原设计、必需责任角色、建议
基线、批准 revision 和私有证据。状态机区分 Pending、Proposed、Approved、Rejected
与 Superseded；推荐值、文档合并和部分签署都不能产生运行授权。

登记表本身不进入运行时。未来实现只接受由 validator 验证、绑定 provider/环境且未
过期的不可变 policy revision。当前全部 gate 为 blocked；本文阶段不选择 IdP、不创建
凭据、不连接外部 tenant，也不修改 F～J 运行代码。全部 Pending 输入完成企业签署后，
首个实现切片按既定顺序从 P2.5-F 会话数据结构与 provider-neutral seam 开始。

个人演示的 51 项实现参数另见
[个人演示身份策略](personal-demo-identity-profile.md)。其 `personal-demo-v1` revision
只适用于虚构资产和本机 `ENVIRONMENT=dev`，企业登记仍全部 Pending。`DemoApproved` profile
只允许 default-off 的实现/测试，staging/production 必须拒绝该 profile，企业 manifest
也不得接收 demo/simulation 结果。

P2.5-F1 首个实现 PR 新增会话 migration 和 `internal/session` 核心，以注入 clock 的
`Establish`、`Authenticate`、`Revoke` 隐藏到期、轮换、撤销和失败关闭。该切片不接管
现有 JWT/Cookie 或 OIDC 登录；通过空闲/绝对到期、重放、并发、逐会话/全会话撤销及
存储故障测试后，再分别接入凭据、认证保证/重新认证、退出和 demo 迁移。

实现使用 `0021_platform_sessions` 保存 credential SHA-256，而非 bearer 明文；记录只含
内部 user/tenant 定位和认证状态，不复制 role/capabilities。PostgreSQL adapter 对活动
更新和 credential replacement 使用 generation CAS；并发撤销/轮换的 CAS miss 直接
deny，数据库故障返回 unavailable。当前/主体撤销都要求非空 correlation ID，并与
撤销状态在同一 PostgreSQL 操作中持久化；建立/轮换也要求 correlation ID。重新认证
只轮换 credential 和认证时间，不重置最初创建时间或绝对到期。主体撤销、新建和轮换
通过内部用户行锁串行，并由 `revoked_before` 水位拒绝撤销前的旧认证证据。

P2.5-F2 在 Query API 认证 seam 中按显式 `ps1_` 前缀路由预先建立的 opaque
credential；前缀命中后只调用 `session.Manager.Authenticate`，失败时不得回退 JWT。
允许结果再按内部 user ID 查询当前用户，校验 active 与 tenant 一致，并仅从当前 role
计算 scopes，session 记录不成为权限快照。

P2.5-F3a 通过 HTTP method + 路由模板把身份绑定、用户/角色/密码、tenant 创建、文档
删除/发布、Agent 批准和 generation rollback 映射到已登记的 high-risk action；其他
请求仍使用 standard `platform.request`。当有效 `ps1_` 会话的认证时间达到 10 分钟
边界时，`session.Manager` 返回 reauthenticate 及不含权限快照的内部 user/tenant 定位。
adapter 必须先重新检查当前用户 active 与 tenant，再返回结构化
`401 reauthentication_required`；无效/撤销/到期/存储故障仍是普通 unauthorized。
迁移期 JWT 保持兼容，不因此获得 MFA 声明。

Query API 仅在 `SESSION_CORE_ENABLED=true`、`ENVIRONMENT=dev` 且 profile 为
`personal-demo-v1` 时构造 PostgreSQL session adapter；默认关闭时仍直接使用现有 JWT
verifier。F2 不签发 `ps1_` credential，登录仍返回 JWT，也不修改 Cookie、OIDC callback、
logout、部署或 staging/production。

F3a 只定义风险判断和 HTTP 响应契约；不解析 `acr`/`amr`/`auth_time`，不创建
state/nonce/PKCE 重新认证事务，不轮换 credential，也不修改 Cookie 或 OIDC callback。

P2.5-F3b 在现有 `oidcauth.Flow` seam 增加独立的重新认证开始/完成命令，不改变普通
登录接口。开始命令创建新的 state、nonce 和 PKCE verifier，并将当前 opaque credential
的 SHA-256 摘要、内部 tenant/subject、策略 action 与安全 return path 写入 TTL 有界的
一次性事务；authorization request 固定要求 `prompt=login`、`max_age=0` 和 demo
`acr_values=2`。内存与 Redis adapter 共用同一事务模型，Redis 通过原子 `GETDEL` 支持
跨实例回调且阻止重放。

OIDC adapter 只在 `AuthenticateWithEvidence` 中解释提供方声明。个人演示 profile 精确
接受 `acr=2`、不多不少的 `pwd` + `otp` `amr` 和 10 分钟内的 `auth_time`，转换为
`AuthenticationEvidence{Assurance: "demo-mfa"}`；原始声明不进入 Flow、session manager
或业务处理器。完成命令在 token exchange 前校验 state 和当前 credential，在 exchange 后
核对 tenant/subject 与证据新鲜度；事务类型混用、绑定变化、重放和依赖故障均失败关闭。

F3b 仍是未装配的事务核心：不注册 HTTP 路由、不改 Web Cookie、不调用
`session.Manager.Establish` 轮换 `ps1_` credential，也不启用 logout、Compose 或生产配置。
后续 F3c 负责将成功结果与原会话的原子 credential rotation、callback 和 Cookie 更新编排。

P2.5-F3c 在 Query API 增加受 session middleware 保护的 reauthentication start/callback
handler。中间件只把服务端分类的 high-risk action 加入 `401 reauthentication_required`；
前端不复制 method/path 风险矩阵。start handler 只接受当前有效、联邦、达到 freshness
边界的 `ps1_`，实时检查内部用户后调用 F3b Flow。JWT、本地、新鲜、撤销/到期 credential
以及 standard/未知 action 都不能生成 transaction。

callback 复用普通登录的 OIDC redirect URI，但只在 callback state 与独立 HttpOnly
reauth-state Cookie 匹配时进入重新认证分支；遗留或不匹配的 Cookie 不得劫持普通登录。
Query API 先完成 F3b transaction，再验证 action 仍为已登记 high-risk、用户仍 active 且
tenant 未变，最后以 `session.Manager.Establish(ReplacesCredential)` 原子 CAS 轮换；成功
只返回新的版本化 `ps1_`、绝对到期、action 和安全 return path。审计 correlation 使用
state SHA-256，日志不保存 state、code、bearer 或 provider claims。

Web BFF 只接受 Origin host/protocol 与请求一致且 `Sec-Fetch-Site=same-origin` 的 POST
start。成功 callback 才覆盖 `ai_etl_token`，Cookie 为 Secure/HttpOnly/SameSite=Lax，寿命
取 30 分钟与后端绝对剩余寿命的较小值；所有失败仅清 reauth state，不改原 credential。
客户端跳转 IdP，但回调后不自动重放原有写操作，用户必须重新确认。

P2.5-F4 在相同 session adapter seam 上接管初始登录签发。只有 Query API 实际构造
`session.Manager` 时，password/OIDC handler 才调用 `Establish` 并返回 `ps1_`；nil adapter
继续签发 JWT。password 证据固定为 `local-password`，session policy 允许它建立普通会话，
但 high-risk action 只有 `demo-mfa` 才满足保证，因此本地密码不能虚构 MFA 或完成联邦
重新认证。OIDC 迁移登录使用 `CompleteLoginWithEvidence`，start 同时请求 fresh login 与
`acr_values=2`，缺失、弱、含糊或陈旧证据失败关闭。

Compose 将同一个 `SESSION_CORE_ENABLED` 传给 Query API 和 Web。Web 在开启时拒绝非
`ps1_` 或过期登录响应，主 Cookie 强制 Secure/HttpOnly/SameSite=Lax，`maxAge` 取 30 分钟
与后端绝对剩余寿命的较小值；关闭时保留 JWT 兼容。任何建立失败不签发 JWT、不写 Cookie。

P2.5-F5 复用 `session.Manager.Revoke(RevokeCurrent)` 作为唯一会话状态变更 seam。
`POST /v1/auth/logout` 位于认证中间件外，因此用户被停用后仍能退出；它只按显式 `ps1_`
前缀路由，未知或已撤销 credential 幂等成功，session store 故障返回 503 且不泄露凭据。
服务端生成 logout correlation，撤销状态与 correlation 由 PostgreSQL adapter 在同一更新中
持久化。遗留 JWT 不伪装成状态化会话，仅保留由 Web 清 Cookie 的兼容行为。

联邦会话始终先完成本地撤销，再可选调用 OIDC Flow。adapter 只接受与 issuer 同 HTTPS
origin 的 discovery `end_session_endpoint`，logout callback 必须与登录 callback 同 HTTPS
origin；不发送或保存 `id_token_hint`。Flow 使用独立、TTL 不超过 15 分钟的 logout
transaction，内存或 Redis `GETDEL` 单次消费 state，并拒绝与登录/重新认证事务混用。
provider metadata、事务或退出能力缺失不恢复本地会话。

Web BFF 的 logout 只接受显式同源 POST，把 HttpOnly Cookie 内的 credential 转给 Query
API。后端成功后才使用匹配属性和 `Max-Age=0` 清主 Cookie；网络或存储失败保留 Cookie。
可选 provider state 使用路径限定的 Secure/HttpOnly/SameSite=Lax Cookie，callback 无论
成功失败都清除该 Cookie，并只跳转至经过双层校验的本地路径。F5 不实现 back-channel
logout、最多三会话、会话/设备列表、指定设备撤销或 staging/production 启用。
