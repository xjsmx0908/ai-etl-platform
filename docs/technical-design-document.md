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
