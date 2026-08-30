# Architecture Design Document

## Context

The existing Go Query API owns upload, retrieval, document registry, and RAG
generation. Qdrant and Elasticsearch store immutable chunk payloads while
PostgreSQL stores authoritative document metadata.

## Knowledge Catalog Module

`internal/knowledgecatalog` is the policy module. Its small interface resolves a
query space, authorizes an upload destination, lists accessible spaces, and
filters retrieved document IDs against authoritative publication state. A
PostgreSQL adapter implements the interface; tests use an in-memory adapter at
the same seam.

Callers provide a principal (`tenant_id`, `user_id`, global role) and an optional
space ID. The module returns a resolved space or a typed forbidden,
unavailable, or not-found error. It owns membership and default-space rules so
upload, query, and HTTP handlers cannot diverge.

## Query Flow

1. Authenticate the principal.
2. Resolve an explicit or default knowledge space.
3. Retrieve with tenant, permission, and space filters.
4. Fail-closed batch validation retains only candidates matching the
   PostgreSQL published release and its healthy active generation.
5. Generate the answer, then verify both evidence support and responsiveness to
   the question before returning citations.

The resolved space is selected before retrieval. Search scores have no role in
authorization or scope selection.

## Data and Compatibility

PostgreSQL gains `knowledge_spaces`, `knowledge_space_members`,
`documents.knowledge_space_id`, and `documents.publication_status`. Legacy scope
metadata remains during P0 for rollback and index compatibility. Migration
creates a default `user-uploads` production space per tenant and quarantines
`enterprise-demo` as a non-default demo space.

This design preserves the accepted lean Compose architecture. Kubernetes or a
service mesh is not required for knowledge correctness.

## Identity Principal Module

`internal/auth` exposes one provider-neutral `Authenticator` interface that
returns a policy-owned `Principal`: internal tenant and subject IDs, internal
role, authentication method, and capabilities. Shared HTTP middleware consumes
only this interface, so future OIDC and service-identity adapters do not own or
duplicate authorization context construction.

The current platform-token adapter marks local-login sessions as `local` and
offline evaluator credentials as `test`. For known subjects, tenant, role, and
capabilities are always rebuilt from PostgreSQL; signed token claims cannot
elevate internal authority. Production rejects test and legacy credentials,
unknown subjects, inactive users, and token-version mismatches. Non-production
profiles retain explicit evaluator compatibility.

Knowledge-space membership remains authoritative in `knowledgecatalog`, not in
the principal or browser token. The OIDC adapter and production local-login
shutdown behavior are implemented below. SCIM, provider group mapping, service
identity, production IdP selection and enablement, MFA/session policy, and
break-glass controls remain gated by ADR 0009's open enterprise decisions.

`internal/externalidentity` adds the provider-neutral mapping below the future
credential adapter. A normalized HTTPS issuer and exact, case-sensitive subject
map to one internal user. Resolution joins the current user row, so disabling a
user or changing their tenant/role takes effect immediately; missing, invalid,
inactive, and unavailable mappings fail closed. The binding stores no copied
role or capabilities.

Tenant administrators manage bindings through
`/v1/users/{user_id}/external-identities`. The module rechecks administrator
role and tenant ownership even after HTTP authorization. Creates and deletes
commit with their audit event in one PostgreSQL transaction, and audit details
omit the external subject. This mapping seam does not validate OIDC tokens or
enable federation by itself.

`internal/oidcauth` is the provider adapter above that mapping seam. It hides
discovery, exact issuer/audience/RS256 validation, JWKS selection and bounded
rotation refresh, authorization-code exchange, PKCE, nonce, and single-use
browser transactions behind one code-exchange interface. Provider claims yield
only `(issuer, subject)`; the directory still owns the transition to an
internally authoritative `Principal`.

The Web BFF owns browser redirects and HttpOnly cookies. It stores only an
opaque state cookie in the browser and exchanges the callback server-to-server;
provider tokens and the platform session are never exposed to browser
JavaScript. Development may use an in-memory transaction adapter. Staging and
production use Redis `GETDEL` so a callback can land on another replica while
remaining single-use. OIDC is disabled by default, and enabling it in production
disables ordinary password login. This is not a break-glass implementation.

`internal/identitylifecycle` is the provider-neutral write-side authority for
provisioned people. Its PostgreSQL adapter owns the atomic transition across the
internal user, exact external binding, retained provider-resource tombstone,
session revocation, audit, and idempotent response. `internal/scim` is a narrow
protocol adapter above that seam; it cannot choose tenant, role, or capability.
Connector policy fixes one tenant, issuer, explicit subject attribute, and
least-privilege default role before requests are accepted.

SCIM and OIDC therefore meet only through the durable `(issuer, subject)`
binding and current user state. Deactivation immediately makes directory
resolution fail and invalidates existing platform sessions. The adapter and
configuration remain default-off until a selected provider passes the real
lifecycle-to-login acceptance gate.

Provider selection does not add provider-specific fields to these interfaces.
Entra ID, Okta, Keycloak, or a separately operated SCIM bridge are true external
dependencies represented only by their protocol adapters. The production
acceptance seam is the composed observable lifecycle: SCIM command to durable
internal identity to current OIDC login decision. Tests and promotion evidence
cross that same seam, preserving provider replacement without duplicating
tenant or authorization policy.

拟议的 P2.5-F `session` 模块位于凭据认证之后、高风险处理器之前。其小型接口
建立、认证、重新认证和撤销平台会话，并隐藏空闲/绝对到期、认证保证新鲜度、
轮换及持久撤销。提供方适配器把精确 `acr`、`amr`、`auth_time` 语义转换为
与提供方无关的认证证据；原始声明不得进入处理器或授权接口。路由注册提供策略
操作/风险类别，并接收 allow、deny 或 reauthenticate。

每个生产请求都必须通过持久注册表解析浏览器凭据中的唯一会话 ID；状态不可用时
失败关闭。PostgreSQL 用户和知识目录状态仍是可变访问权限的权威来源。本地撤销
先于可选 IdP 退出，因此提供方故障不能保留平台访问权。批准后的寿命、认证保证
映射和迁移批次仍是外部输入；当前无状态 24 小时 JWT 是已核实的现状，而不是
目标企业会话架构。

拟议的 P2.5-G `emergencyaccess` 模块与普通 OIDC、本地登录和 `admin` scope
分离。外部保管适配器提供多方签署的单次激活证明，操作人再用独立恢复认证器证明
持有权；模块原子创建短期事故租约、会话、不可变审计和通知 outbox。每个请求只
获得租约中明确列出的租户级恢复动作，并重新校验操作者、租约、会话、动作、到期
和撤销状态。恢复命令在副作用前持久化幂等意图和审计，再调用模块内部的受限恢复
适配器并记录结果；外部操作的不确定结果由操作日志收敛。审计或租约状态不可用时
失败关闭。

恢复身份没有常驻平台角色或内容访问能力，不能复用 bootstrap admin、普通本地
密码、手工 JWT 或 SCIM/OIDC 用户。主 IdP 故障由该模块处理；PostgreSQL、审计
存储或控制面故障属于独立基础设施灾难恢复，不通过应用后门解决。人数、quorum、
时限、认证器、动作白名单和保管位置保持企业输入 `Pending`。

拟议的 P2.5-H `groupdirectory` 模块把选定提供方的 SCIM Groups 或后台目录数据
转换为连接器范围内完整、有版本的组成员快照。OIDC raw group claim 只用于诊断，
不能授权。`groupmapping` 模块由租户管理员把稳定 `(connector_id,
provider_group_id)` 映射到同租户知识空间的 reader/contributor/manager；显示名、
邮箱域、组路径和提供方组织均不能选择租户、平台角色或 scope。

现有 `knowledge_space_members` 保持直接用户授权来源；组、成员和映射使用独立来源
记录。`knowledgecatalog` 在每次 Resolve/List 内合并直接授权与当前完整组快照的
活跃映射，同一空间按 reader < contributor < manager 取最高角色，并保留来源
解释。删除一个来源不会误删另一个来源；最后来源撤销、用户/组/空间/连接器停用或
快照超过批准陈旧窗口后，下一次请求失败关闭。授权结果不进入长寿命平台会话。

## Version-bound Publication Module

`internal/publicationworkflow` owns governed publication behind the assessment
and approved-publication interface. PostgreSQL resolves only the current
release version's sealed, active generation whose Qdrant and Elasticsearch
observations match the expected chunk count and identity digest. The returned
candidate is immutable: document version, generation, expected count/digest,
and release revision. Tenant scope always comes from authenticated context.

Agent approval persists that complete candidate. Approved publication locks
and revalidates it, then updates the document state, advances the release
compare-and-set, binds the idempotency key, and appends the audit event in one
transaction. Cache invalidation runs after commit.

Query enforcement of the published release is applied by the retrieval engine
and the Elasticsearch document-search handler. Both use the same resolver, so
semantic cache, Qdrant, Elasticsearch, and content-search results cannot expose
a replacement generation before approval. A replacement keeps the old release
visible until atomic cutover; `user-uploads` records an explicit automatic
release after successful activation.

## Recoverable Deletion Module

`internal/deletionworkflow` owns deletion from HTTP acceptance through final
cleanup. Its small interfaces separate the PostgreSQL state machine from three
idempotent dependency adapters. Acceptance atomically marks the document
deletion-pending, revokes the published release, cancels queued ingestion, and
records the audit/job correlation, making all query paths fail closed before
external deletion begins.

The worker claims bounded batches with a lease and fencing token. Qdrant and
Elasticsearch are deleted by tenant/document identity; MinIO receives the exact
immutable keys captured at acceptance. Each success is durable across retries.
An active ingestion lease delays the claim, and only a fully successful current
claim removes the authoritative PostgreSQL graph.

## Governance Acceptance Boundary

`scripts/governance-acceptance.sh` is the executable P2.4 system boundary. It
starts an isolated stack and drives authenticated public HTTP through managed
admission, independent approval, release continuity and cutover, recoverable
deletion, and correlated audit. Compose is used only to interrupt dependencies;
database inspection is not accepted as business-success evidence.

Deterministic inference keeps the lifecycle repeatable while PostgreSQL, Kafka,
Qdrant, Elasticsearch, and MinIO remain real. The runner retains a timestamped,
secret-free JSON report and always tears down its project unless explicitly kept
for diagnosis. This gate proves governance orchestration, not production answer
quality, capacity, identity federation, or recovery objectives.
