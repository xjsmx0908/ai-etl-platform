# Backlog

## 2026-08-27 - Enterprise RAG Implementation Baseline

Status: approved; P2.2, P2.3, and P2.4 engineering implementation complete;
later phases still require their ADR and business-input gates

Goal: turn the accepted enterprise RAG direction into staged, testable work
without treating unknown identity, quality, capacity, or recovery requirements
as engineering assumptions.

Proposed decisions:

1. [ADR 0007](adr/0007-transactional-outbox-ingestion.md) makes PostgreSQL
   admission and its transactional outbox the durable start of every accepted
   ingestion job.
2. [ADR 0008](adr/0008-generation-scoped-index-manifests.md) makes
   generation-scoped manifests authoritative for completeness of Qdrant and
   Elasticsearch projections.
3. [ADR 0009](adr/0009-enterprise-identity-federation.md) introduces
   provider-neutral enterprise identity while keeping authorization and
   knowledge-space policy in existing modules.
4. [ADR 0010](adr/0010-production-slo-and-recovery-gates.md) blocks production
   promotion until business-owned SLO, recovery, retention, capacity, and
   quality objectives have current evidence.
5. [ADR 0011](adr/0011-version-bound-governance-publication.md) unifies managed
   publication, exact document versions, generation identity, replacement, and
   recoverable deletion behind one PostgreSQL-owned release lifecycle.

Required external inputs:

1. Name the first production business scope and the owner authorized to approve
   the private enterprise Gold artifact.
2. Select the identity provider and approve tenant, group, provisioning, MFA,
   session, and break-glass policy.
3. Approve numeric SLO, RPO/RTO, retention, capacity, data-residency, and budget
   constraints.

Staged implementation after ADR approval:

1. **P2.2 Durable admission:** add versioned object reservations, PostgreSQL
   ingestion jobs/outbox, an at-least-once relay, idempotent consumer handling,
   orphan cleanup, metrics, and crash-point tests.
2. **P2.3 Index generations:** add durable manifests, deterministic identity
   digests, dual-backend verification, atomic activation, reconciliation,
   rollback, and generation cleanup.
3. **P2.4 Governance acceptance:** extend automated E2E through managed upload,
   independent approval, active-generation publication, query, replacement,
   deletion, audit, and dependency-failure recovery.
4. **P2.5 Enterprise identity:** implement the approved OIDC and provisioning
   adapters, dual-auth migration, production local-login disablement, service
   identity, and security-negative tests.
5. **P2.6 Production evidence:** implement the approved dashboards, alerts,
   load and quality gates, backup/restore automation, failure exercises, and
   retained release evidence.

Global acceptance gates:

- Every successful upload admission is recoverable from PostgreSQL without
  inferring governance data from a search index.
- A document is publishable only when the active generation exactly matches the
  expected chunk identity set in both search projections.
- Cross-tenant, unknown-user, draft, retired, stale-generation, and dependency-
  failure paths fail closed without leaking content.
- Mock evaluation remains an integration gate; production quality claims use a
  signed business Gold artifact and real models.
- Full Go/Python/Web tests, Compose validation, security scanning, deterministic
  evaluation, real-model acceptance, load testing, and recovery exercises pass
  before production promotion.

P2.2 progress (2026-08-27):

- Implemented the first vertical slice: PostgreSQL atomically commits the
  document catalog row, ingestion job, and task outbox event before HTTP `202`.
- Uploads use immutable version keys; stable idempotency identities recover a
  committed admission after a gateway crash without creating a second job.
- A leased, multi-replica-safe relay publishes committed events at least once;
  Kafka outages leave events pending with bounded retry backoff.
- Implemented consumer terminal-state/deduplication: durable jobs use processing
  leases, atomically update job/document terminal state before Kafka ACK, skip
  terminal redelivery, and recover expired work. Kafka commits now advance only
  over the fetched partition prefix whose tasks reached terminal handling.
- Aligned the deployed ingestion lease with the Compose pipeline retry window:
  the worker now defaults to a 75-minute lease for a roughly 60-minute worst
  case, and a configuration regression test guards Compose and example files.
- Added bounded-cardinality PostgreSQL snapshots and Prometheus metrics for
  outbox backlog/retries/age, durable job states, and expired processing leases,
  plus sustained-failure alert rules verified with `promtool`.
- Added scheduled, bounded orphan collection for old versioned objects that are
  absent from both the document catalog and every admitted ingestion job.
  Reference lookups are index-backed and fail closed; MinIO batches retain a
  fair scan cursor. Admitted terminal versions remain protected until an
  explicit generation-retention policy owns their deletion.
- Added and passed isolated crash-point E2E: Kafka and the worker are stopped,
  upload still returns HTTP `202`, the API/relay is then stopped, and restarting
  Kafka/API/worker eventually reaches `completed:published` through public APIs.
- Remaining: ADR 0008 generation activation before failed mid-index
  replacements can be called atomically safe. Outbox retention/archival and
  additional crash points remain later operational hardening, not claims of
  this slice.

P2.2 third-slice implementation plan (approved 2026-08-27):

1. Expose durable PostgreSQL snapshots for pending/retried outbox events, oldest
   event age, job states, and expired processing leases; publish them through
   bounded-cardinality Prometheus metrics.
2. Alert on sustained old outbox work, repeated publication attempts, and
   expired processing leases, with `promtool` rule tests.
3. Add a scheduled object collector that deletes only versioned objects older
   than a grace period and proven unreferenced by the current document catalog.
4. Add an isolated crash-recovery acceptance script that starts from HTTP `202`,
   interrupts relay/worker progress, and verifies eventual terminal state via
   public APIs after restart.

No implementation phase starts merely because this backlog entry exists. Each
ADR must first be reviewed, its open decisions resolved, and its phase-specific
plan approved.

P2.3 progress (2026-08-28):

- Added a generation manifest schema containing immutable build configuration,
  expected count/digest, Qdrant and Elasticsearch observations, lifecycle,
  attempts, errors, and verification/activation timestamps.
- Added an order-independent digest over generation/document identity, chunk
  index/ID, and SHA-256 content hash; invalid or duplicate chunk identities fail
  closed.
- Added a narrow PostgreSQL adapter for create, observe, fail, ready, and atomic
  activation transitions. A partial or mismatched observation cannot become
  ready, and activation rollback preserves the prior active generation.
- Hardened the persistence seam after review: manifest creation now supports
  idempotent crash replay with immutable-definition conflict detection; build
  configuration is mandatory; document versions reference durable ingestion
  jobs; failed builds can be retried after clearing observations; stale
  activation writers are rejected using expected-current CAS. A real PostgreSQL
  concurrency test verifies that only one competing activation succeeds.
- Subsequent slices at this checkpoint were reconciliation/repair,
  rollback/retention cleanup, bounded metrics/alerts, and the full ADR
  acceptance matrix. Reconciliation/repair is now implemented below.

P2.4 approved plan (2026-08-29; P2.4-A and P2.4-B implemented):

The governing decision is [ADR 0011](adr/0011-version-bound-governance-publication.md).
P2.4 integrates the existing P2.1 publication workflow with P2.2 durable
versions and P2.3 generation manifests; it does not rebuild the RAG platform.
Repository inspection found three correctness gaps that must precede an E2E
claim:

- publication readiness still trusts document-level backend counts instead of
  the sealed, healthy active-generation identity;
- durable approval names only `document_id`, so a replacement during review can
  change the bytes ultimately published; and
- replacement can leave multiple version-scoped active generations visible,
  while deletion has no durable record for partial dependency failure.

Test-driven delivery is split into reviewable, stacked changes:

1. **P2.4-A — Version-bound release model.** Add the current-version and
   published-release identity/revision schema with deterministic legacy
   backfill. Add PostgreSQL integration tests for one visible release,
   compare-and-set updates, tenant isolation, and ambiguous-backfill failure.
2. **P2.4-B — Exact-candidate approval.** Replace count-only readiness with a
   small publication-candidate interface backed by the active manifest and its
   expected/observed identity. Persist version, generation, digest, and revision
   in approval arguments; reject stale, self, cross-tenant, unhealthy, and
   replayed candidates. Commit publication and its audit event atomically.
3. **P2.4-C — Replacement and read cutover.** Require managed query candidates
   to match the published release. Preserve the old approved release while a
   replacement builds or awaits approval; cut over only after exact-candidate
   approval. Preserve the automatic `user-uploads` policy through the same
   explicit release identity. Cover caches and both retrievers.
4. **P2.4-D — Recoverable deletion.** Add a durable, leased deletion workflow
   with immediate fail-closed visibility, per-Qdrant/Elasticsearch/object-store
   progress, idempotent retry, immutable audit correlation, metrics, and tested
   alerts. Do not enable generation retention.
5. **P2.4-E — Governance acceptance.** Add an isolated public-interface E2E
   harness and acceptance matrix for managed upload, independent approval,
   publication/query, replacement continuity and cutover, deletion, audit, and
   recovery after Kafka, Qdrant, Elasticsearch, MinIO, or process interruption.
   CI runs deterministic module/PostgreSQL tests; full dependency exercises are
   an explicit acceptance command with retained, secret-free results.

Definition of done:

- Every governed approval is cryptographically and transactionally bound to
  the exact document version and generation that was assessed.
- Query visibility has one PostgreSQL-owned release identity and fails closed
  for drafts, stale generations, deletion, unknown identity, or authority
  lookup failure without leaking cross-tenant content.
- Replacement preserves the last approved release until atomic approved
  cutover; stale approval cannot publish a newer replacement.
- Deletion is restart-safe and exposes partial progress without reporting
  success before every owned dependency acknowledges cleanup.
- Focused tests are written first for each slice, then the full Go race suite,
  `go vet`, Python/Web tests, Compose validation, security scan, deterministic
  eval, generation acceptance, and governance acceptance all pass.
- Documentation and the append-only learning log match implemented behavior.
  Production deployment, retention enablement, and P2.5 identity work remain
  out of scope.

Expected implementation areas (exact files may be refined after red tests):
`internal/migrations`, `internal/publicationworkflow`, `internal/indexmanifest`,
`internal/query`, `internal/agentapi`, `internal/audit`, `cmd/api`, worker
wiring, `scripts`, `infrastructure/rules`, and governance/architecture docs.
ADR 0011 and this plan are approved. Each subsequent slice remains separately
reviewable and must pass its own test and merge gates before the next cutover.

P2.4-A outcome (2026-08-29):

- Added a PostgreSQL-owned release record that separates the latest admitted
  version from the exact version/generation currently approved for queries.
- Durable admission records `current_version_id` in the same transaction as
  the document, ingestion job, and outbox event. Same-version replay is
  idempotent; replacement advances the revision without changing the published
  release.
- Added tenant-scoped lookup and revision-based publication CAS. A stale
  candidate cannot publish after any intervening replacement or publication.
- Added deterministic migration behavior: a unique legacy object/job plus
  active generation is backfilled, multiple candidates abort migration, and
  absent historical proof is retained explicitly as `unresolved`.
- CI runs the new PostgreSQL integration tests against PostgreSQL 16. Full Go
  tests, race detection, formatting, and `go vet` pass. Query visibility and
  approval behavior remain unchanged until P2.4-B/C consume this release seam.

P2.4-B outcome (2026-08-29):

- Replaced mutable document-level backend counts with one PostgreSQL candidate
  for the current release version's sealed, active, reconciled generation.
- Assessment and the durable Agent approval now carry document version,
  generation, expected chunk count/digest, and release revision. Tenant scope
  remains derived from the authenticated actor rather than tool arguments.
- Publication locks and revalidates the exact manifest, then changes document
  publication state, advances the release CAS, stores the idempotency binding,
  and appends the success audit in one transaction. Stale, unhealthy, or
  altered candidates produce no partial writes; response-loss replay is safe.
- Removed the obsolete HTTP document-count inspector and non-transactional
  best-effort publication adapter. Query visibility remains P2.4-C scope.

P2.4-C outcome (2026-08-29):

- Query and content-search candidates now pass through the PostgreSQL-owned
  published-release resolver. A candidate is visible only when its exact
  document version and generation match the resolved release and the active
  manifest remains sealed, healthy, and reconciled in both projections.
- A managed replacement advances the current version without retiring the
  published release. The old version remains queryable until an independent,
  exact-candidate approval atomically cuts over the release.
- Completed `user-uploads` versions use the same explicit release identity via
  an idempotent automatic publication transition; managed spaces still require
  approval. Replacement uploads cannot change permission or knowledge space.
- Added handler, release, ingestion, and PostgreSQL continuity tests. P2.4-D
  recoverable deletion and P2.4-E public-interface acceptance remain next.

P2.4-D implementation plan (approved 2026-08-29):

1. Replace synchronous document deletion with one PostgreSQL acceptance
   transaction that revokes the published release, marks the catalog row
   deletion-pending, creates or replays a deletion job, and appends its immutable
   audit event. HTTP returns acceptance only after this fail-closed commit.
2. Put job claiming, lease/fencing, per-Qdrant/Elasticsearch/object-store
   progress, retry diagnostics, and final authoritative cleanup behind one
   `deletionworkflow` store interface.
3. Run a bounded collector in the existing worker process. Dependency deletes
   are exact and idempotent; a stale lease cannot record progress, and the
   document/release/job rows are removed only after every dependency succeeds.
4. Add bounded PostgreSQL snapshots, Prometheus metrics, and alerts for pending,
   failed, old, and expired-lease deletion work. Retention remains disabled.
5. Verify through the accepted seams: `DELETE /v1/documents/{id}`, the durable
   deletion store, and collector dependency adapters. Cover tenant isolation,
   request replay, immediate query invisibility, partial failure/restart, stale
   fencing, and final cleanup before the full project quality suite.

P2.4-D outcome (2026-08-29):

- `DELETE /v1/documents/{id}` now accepts one durable deletion job and returns
  HTTP `202`. The same PostgreSQL transaction retires the document, revokes the
  published release, cancels unstarted ingestion, and appends a correlated
  audit event; replay returns the existing job.
- The worker runs a leased collector with fencing and durable progress for
  Qdrant, Elasticsearch, and exact immutable object keys. Successful dependency
  work is not repeated; failures remain pending and observable for retry.
- Active ingestion leases delay cleanup, while ingestion completion and managed
  approval cannot republish a deletion-pending document. The authoritative
  document/release/job graph is removed only after all dependencies succeed.
- Added bounded deletion metrics and tested stalled, dependency-failure, and
  expired-lease alerts. Generation retention remains disabled and independent.
- P2.4-E public-interface governance acceptance remains next.

P2.4-E implementation plan (approved 2026-08-29):

1. Define one isolated governance-acceptance command and a scenario matrix whose
   observation seams are authenticated HTTP responses and a retained JSON
   report. Database state is not accepted as user-visible success evidence.
2. Drive a managed-space document through durable upload, ingestion, an
   independently administered exact-candidate approval, query visibility,
   replacement continuity, approved cutover, recoverable deletion, and audit
   correlation. Use deterministic model adapters while exercising real
   PostgreSQL, Kafka, Qdrant, Elasticsearch, and MinIO containers.
3. Inject bounded dependency/process interruptions through Docker Compose and
   prove recovery after Kafka/API/worker interruption, search projection
   interruption, and object-store interruption. Every accepted operation must
   remain fail-closed until its owned dependencies recover.
4. Emit a timestamped, secret-free JSON result with scenario status, duration,
   public observations, and failure diagnostics. Add contract tests that map
   every ADR 0011 acceptance requirement to executable evidence.
5. Keep the full dependency exercise opt-in rather than pretending it is a
   lightweight module test. Run deterministic contract tests in normal CI and
   document the explicit full-stack command for release evidence retention.

P2.4-E outcome (completed 2026-08-29):

- Added one isolated executable gate, `scripts/governance-acceptance.sh`, and a
  public-evidence scenario matrix. Normal CI discovers its deterministic
  contract tests; the real dependency exercise remains an explicit command.
- Verified managed admission and ingestion recovery, draft invisibility,
  independent approval, old-release continuity, approved replacement cutover,
  immediate deletion revocation, durable partial cleanup, eventual `404`, and
  publication/deletion audit correlation.
- Injected Kafka/API/worker and Qdrant/Elasticsearch/MinIO interruption against
  real containers. The passing run retained a timestamped JSON report containing
  bounded HTTP/metrics observations and no credentials or authorization data.
- Updated the existing smoke test to accept asynchronous deletion via HTTP
  `202` and poll for final `404`. No deployment or retention enablement occurred.

P2.5-A implementation plan (approved 2026-08-29):

1. Introduce one provider-neutral identity module interface that returns a
   policy-owned `Principal`: internal tenant and subject IDs, internal role,
   authentication method, and granted capabilities. Existing authorization and
   knowledge-space membership remain outside the authentication adapter.
2. Mark platform-issued sessions as `local` and offline evaluator credentials
   as `test`; reserve `federated` and `service` identities without implementing
   a provider adapter in this slice. Preserve development, staging, and explicit
   evaluation compatibility.
3. In the production authentication policy, reject test/legacy credentials,
   unknown internal subjects, inactive users, and token-version mismatches. For
   a known subject, derive tenant, role, and capabilities from the internal user
   record rather than trusting mutable token claims.
4. Test through two approved seams: the identity module interface and protected
   HTTP middleware behavior. Cover claim tampering, unknown/inactive subjects,
   test-token rejection, revocation, and non-production evaluator compatibility.
5. Update configuration and architecture documentation and run the complete
   Go race/vet, Python, Web, Compose, observability, and diff gates. Do not add
   OIDC/SCIM, select an IdP, enable production federation, or deploy this slice.

P2.5-A outcome (completed 2026-08-29):

- Added a provider-neutral `Authenticator` interface and policy-owned
  `Principal`, with shared HTTP middleware that no longer depends on one token
  adapter's claims shape.
- Platform sessions identify `local`, `test`, and legacy authentication. Exact
  HS256 validation rejects alternate HMAC algorithms.
- Known subjects derive tenant, role, and capabilities from PostgreSQL. The
  production policy rejects test/legacy credentials, unknown/inactive users,
  and revoked token versions; non-production keeps evaluator compatibility.
- Added identity-interface, middleware, and Query API construction tests for
  privilege-claim injection and every fail-closed path in this slice.
- OIDC/SCIM, service identities, production local-login disablement, provider
  mapping, MFA/session policy, and break-glass remain pending IdP/policy input.

P2.5-B implementation plan (approved 2026-08-30):

1. Add a durable, provider-neutral mapping from one normalized external
   `(issuer, subject)` pair to exactly one tenant-scoped internal user. Enforce
   database uniqueness and tenant/user referential integrity; retain subject
   case because OIDC subject identifiers are case-sensitive.
2. Put normalization, lookup, active-user validation, and fail-closed error
   semantics behind a narrow identity-resolution module interface. Resolve
   mutable tenant and role authority from the current internal user row rather
   than copying it into the external binding.
3. Add tenant-admin HTTP management for listing, creating, and deleting a
   user's bindings. The module independently enforces administrator role and
   tenant ownership. Binding mutations and their security audit records commit
   in the same PostgreSQL transaction; audit failure rolls back the mutation.
4. Test through the two approved seams: the identity-resolution module
   interface and authenticated/authorized HTTP management. Add real PostgreSQL
   integration coverage for normalization, uniqueness, current role and active
   state, tenant isolation, and transactional audit.
5. Update identity architecture and operating documentation and run the full
   repository gates. Do not select or integrate an IdP, validate OIDC tokens,
   add JWKS/SCIM/JIT/group mapping, enable production federation, or deploy.

P2.5-B outcome (completed 2026-08-30):

- Added a tenant-safe external identity binding schema with global
  issuer/subject uniqueness and a composite user/tenant foreign key.
- Added a provider-neutral directory that returns current internal authority
  and fails closed for invalid, unknown, inactive, or unavailable identities.
- Added admin-only list/create/delete routes. The management module independently
  enforces role and tenant ownership; create/delete and subject-free audit
  records commit in one PostgreSQL transaction.
- Added module, protected HTTP, migration, and real PostgreSQL tests. OIDC/JWKS,
  SCIM/JIT, provider/group selection, production federation, and deployment
  remain gated by ADR 0009 decisions.

P2.5-C implementation plan (approved 2026-08-30):

1. Add a provider-neutral OIDC adapter whose small authentication interface
   accepts a validated authorization-code result and returns the existing
   policy-owned `Principal`. Use standards-based discovery and JWKS with
   Keycloak as the first acceptance provider, while keeping provider-specific
   claims out of tenant, role, and capability decisions.
2. Test the authentication seam first. Require exact issuer and audience,
   explicit asymmetric signing algorithms, expiry and nonce validation,
   external `(issuer, subject)` binding resolution, bounded JWKS refresh for
   key rotation, and fail-closed behavior when discovery, JWKS, token exchange,
   or the internal directory is unavailable.
3. Add an HTTP authorization-code flow with PKCE, cryptographically random
   state and nonce, a short-lived single-use transaction, safe return paths,
   and a federated platform session. The Web BFF remains the browser boundary;
   credentials and provider tokens are never exposed to browser JavaScript or
   persisted as authorization authority.
4. Keep OIDC disabled by default. Development and staging retain local login;
   a production profile with OIDC enabled rejects ordinary password login.
   SCIM/JIT, group-derived authorization, service identities, break-glass,
   production federation enablement, and deployment remain separate reviewed
   slices.
5. Update configuration, Web login affordances, ADR/architecture/operations
   documentation, and the append-only learning log. Run focused red/green
   cycles followed by full Go race/vet, PostgreSQL, Python, Web, Compose,
   observability, deterministic evaluation, and security gates before review.

P2.5-C outcome (completed 2026-08-30):

- Added strict HTTPS discovery, Authorization Code + PKCE, RS256/JWKS token
  validation, exact issuer/audience/nonce checks, and one bounded key-rotation
  refresh behind the provider-neutral code-exchange seam.
- Added single-use browser transactions using memory in development and Redis
  `GETDEL` outside development, federated platform sessions, safe return paths,
  and Web BFF redirects with HttpOnly state and session cookies.
- Production with OIDC enabled now rejects password login and non-federated
  platform sessions. Staging retains dual-auth migration; OIDC remains disabled
  by default in every environment.
- Full Go and race suites, real PostgreSQL and Redis integration tests, 128
  script tests, Parser 32, Reranker 1, Webhook 3, Web audit/lint/build, Compose,
  observability, Trivy, and the 47-case deterministic full-stack eval passed.
- The ordinary suite uses a real TLS OIDC protocol test provider. The explicit
  acceptance test also passed a complete HTTPS Authorization Code + PKCE flow
  against Keycloak 26.3.3, including login, callback, code exchange, RS256/JWKS
  validation, and policy-principal resolution. Production IdP selection and
  enablement, SCIM/JIT, groups, service identities, MFA/session policy,
  break-glass, and deployment remain later reviewed work.

P2.5-D identity lifecycle provisioning (implemented 2026-08-30):

1. Put user creation, external binding, deactivation, session revocation,
   tombstone retention, idempotency, and audit behind one provider-neutral
   `Provisioner.Apply` seam with an atomic PostgreSQL adapter.
2. Bind each connector to one internal tenant and a least-privilege default
   role. Never derive tenant, role, or capabilities from SCIM organization,
   email, role, group, or OIDC claims.
3. Add a bounded SCIM 2.0 Users adapter that authenticates with a rotatable,
   connector-scoped secret and translates protocol requests into lifecycle
   commands without directly owning policy tables.
4. Make deactivation immediately set `active=false` and increment
   `token_version`; retain provider/resource and issuer/subject tombstones to
   prevent identifier reuse from taking over historical access.
5. Test through the lifecycle module and SCIM HTTP seams, then require a real
   selected-provider lifecycle-to-OIDC acceptance run before production use.
6. Keep JIT, group authorization, service identities, production enablement,
   MFA/session policy, and break-glass outside this slice. Production waits for
   selected-provider acceptance and the decisions in
   [identity-lifecycle-design.md](identity-lifecycle-design.md).
7. Added migration `0020`, an atomic PostgreSQL lifecycle provisioner, a
   default-off bounded `/scim/v2/Users` adapter, SCIM-owned account safeguards,
   credential overlap, metrics, and sustained-failure/staleness alerts. Unit
   and real PostgreSQL tests cover replay, rollback, revocation, tombstones,
   constrained reactivation, replacement semantics, and HTTP error behavior.
8. Remaining production gate: select the IdP and connector owner, approve its
   stable subject mapping/default role/retention/SLA/rotation policy, then run a
   real SCIM create-update-deactivate-reactivate-to-OIDC acceptance exercise.

P2.5-E enterprise identity production acceptance design (proposed 2026-08-30):

1. Compare Entra ID, Okta, and Keycloak with an evidence-weighted matrix; reject
   any candidate that cannot prove exact SCIM-to-OIDC subject equality, the
   required bounded SCIM Users profile, or OIDC interoperability.
2. Require named identity, tenant, security, privacy, legal, and operations
   owners to approve connector tenancy, subject mapping, default role, stored
   profile, provisioning mode, deactivation SLA, reconciliation, tombstone
   retention, rotation, support, residency, and cost.
3. Specify a public-interface staging sequence covering create, login, update,
   concurrent replay, deactivate/session revocation, reactivation, delete,
   identifier reuse, credential/key rotation, outages, redaction, and metrics.
4. Retain a signed, secret-free acceptance bundle tied to provider configuration,
   platform commit/images, owner, expiry, and rollback rehearsal. Expire it on
   material provider, mapping, or platform identity changes.
5. Keep MFA/session/logout, break-glass, group mapping, service identity,
   selected-provider implementation, production enablement, and deployment in
   separately approved P2.5-F through J slices.
6. Review the proposal in
   [enterprise-identity-production-acceptance.md](enterprise-identity-production-acceptance.md).
7. P2.5-J must add IdP-authoritative reconciliation, distinct last-completed and
   failure evidence, drift/retry/missed-run tests, and the real staging run.
   Existing SCIM last-success age measures mutation activity only and cannot
   satisfy the reconciliation gate.

P2.5-F 企业会话安全设计（2026-08-30 提议）：

1. 精确记录当前限制：固定 24 小时平台 JWT 及匹配的 HttpOnly Cookie、用户级
   `token_version` 撤销和仅清除 Cookie 的退出；尚无会话 ID、注册表、空闲到期、
   认证保证状态、重新认证或逐设备撤销。
2. 把会话建立、认证决策、新鲜度、轮换和撤销放在一个与提供方无关的深层模块后。
   提供方适配器转换精确 `acr`/`amr`/`auth_time` 行为；处理器只看到内部操作及
   allow、deny 或 reauthenticate。
3. 要求持久生产会话注册表，包含唯一会话 ID、独立空闲/绝对/新鲜度时钟、逐会话
   与全会话撤销、当前内部权限及失败关闭查询。
4. 先撤销平台会话，再清除 Cookie 并执行可选且经验证的 RP 发起 IdP 退出。
   IdP 故障不能阻止本地退出。
5. 只有完成精确外部绑定并观察到 staging 证据后才迁移指定批次。生产切换关闭
   密码并使本地会话失效；回滚恢复预先验证的联邦配置目标或获批非紧急恢复路径，
   不得静默恢复密码或绕过 MFA。
6. 认证保证映射、所有数值寿命/上限、批次和切换日期均保持 `Pending`，直到安全、
   身份、产品、隐私、支持和运维责任人批准。P2.5-G 负责紧急访问；P2.5-J 负责
   选定提供方实现和证据。
7. 评审 [企业会话安全设计](enterprise-session-security-design.md)。

P2.5-G 企业紧急访问设计（2026-08-30 提议）：

1. 明确当前 bootstrap admin、普通本地密码、`admin` scope 和手工 JWT 均不构成
   企业紧急访问；生产联邦策略当前没有紧急路径，普通登录审计也是 best-effort。
2. 新增独立 `emergencyaccess` 深层模块设计。外部保管适配器提交多方签署的单次
   激活证明，操作人再用独立恢复认证器完成 challenge 持有证明；证明不是 bearer。
3. 保管能力常备但平台权限不常驻。原子创建短期事故租约、会话、不可变审计和通知
   outbox；逐请求 `Execute` 验证操作者、租约、租户、动作、会话、到期和撤销状态，
   在副作用前持久化幂等操作意图，再由内部受限适配器执行并记录结果。
4. 使用独立 `emergency` 认证方式和最小动作白名单，不继承普通 `admin` scope，
   不允许内容查询/上传、发布、删除、Agent 执行或把临时能力变成常驻角色。
5. 审计或租约存储故障时失败关闭；通知发送可从持久 outbox 重试。结束、绝对/空闲
   到期、延期代次和材料轮换必须撤销所有关联凭据并形成独立复盘证据。
6. 将 PostgreSQL、审计存储和控制面不可用划入独立基础设施灾难恢复，不通过应用
   后门、数据库直改或 shell 绕过。P2.5-J 必须引用仍有效的 canary 演练结果。
7. 所有人数、quorum、时限、认证器、动作、网络、告警和证据保留值保持 `Pending`，
   直到安全、身份、连续性、SRE、SOC、合规、法务和租户责任人批准。
8. 评审 [企业紧急访问设计](enterprise-emergency-access-design.md)。

P2.5-H 企业组到知识空间授权设计（2026-08-30 提议）：

1. 记录当前事实：知识目录只读取直接 `knowledge_space_members`，平台 admin 绕过
   成员表；没有成员管理路由，OIDC 不解析组，SCIM Users 明确拒绝 groups/roles。
2. 设计 `groupdirectory` 完整快照模块。选定提供方适配器处理 SCIM Groups/目录
   分页和差异；模块验证连接器租户、稳定组 ID、成员 subject、版本、完整性和幂等，
   原子更新组/成员/同步状态/审计并为删除组保留 tombstone。
3. 设计租户 `groupmapping` 模块，把稳定 `(connector_id, provider_group_id)` 映射
   到同租户知识空间 reader/contributor/manager。raw claim、组名、域名、路径不能
   选择租户、平台角色、全局 scope、紧急或服务身份能力。
4. 保留现有直接成员作为独立来源；`knowledgecatalog` 每次 Resolve/List 合并直接
   与当前完整组授权，同一空间取最高角色并保留来源。删除一个来源不误删另一来源，
   最后来源撤销后下一请求立即拒绝。
5. token overage、省略、分页不完整、跨页源版本变化、版本倒退和同步失败的候选快照不能替换上一个
   完整版本或解释为权威空集；只有上一个完整快照可在批准陈旧窗口内继续使用，
   超过窗口后组派生授权失败关闭，直接授权保持独立有效。
6. 短期授权缓存绑定映射/快照修订，每次命中前同步校验修订栅栏；事务 outbox 仅作
   加速失效。管理写入需要高风险重新认证、幂等审计和提权审批。先 shadow 比较，再
   按 reader → contributor/manager canary 迁移；回滚按迁移记录逐条 CAS 恢复直接成员，
   与后续人工变更冲突时转对账而不覆盖。
7. 组协议、稳定 ID、嵌套/动态语义、审批、同步/撤权 SLA、上限、缓存和证据保留
   均为 `Pending`；P2.5-J 必须验证真实分页、overage、撤权、对账和回滚。
8. 评审 [企业组到知识空间授权设计](enterprise-group-authorization-design.md)。

P2.5-I 企业服务与工作负载身份设计（2026-08-30 提议）：

1. 记录当前事实：仅预留 `AuthenticationMethodService`，没有服务认证器/注册表；用户
   JWT 无固定 audience，Parser/告警使用共享 token，Reranker 入站未验证，异步执行
   没有统一的人类发起者、执行 workload 和委托模型。
2. 设计 `workloadidentity` 深层模块和显式 human/workload Principal。精确 trust
   domain/issuer/subject、单一 audience 和内部注册 grant 决定能力；provider/cloud
   claim 只能缩小，机器不能伪装 user、选择租户、继承 admin、组或紧急访问。
3. 业务 workload 固定租户，平台 workload 固定环境/platform scope；知识空间使用
   独立 service grant 且必须显式指定空间。数据库/Kafka/存储和供应方凭据只访问对应
   资源，不能转换为平台 Principal。
4. 优先短期 sender-constrained mTLS/SPIFFE 或 mTLS/DPoP 绑定的 OAuth token；
   `private_key_jwt` 不能单独约束 access token，静态/bearer secret 只作有期限迁移
   例外。每次请求同步校验 workload/grant/credential 修订，泄露时立即撤权。
5. Kafka/Agent 使用有期限 `DelegationGrant`，绑定 human initiator、executor
   workload、租户、资源/空间、动作、策略修订和请求摘要；签发与每次副作用都要求
   委托是发起者当前权限与执行器 grant 交集的子集，消息不携带浏览器 token，
   workload 不能自审批。
6. 先资产清单和 shadow，再迁移 Parser/Reranker/告警、业务自动化、异步委托及基础
   设施 ACL/TLS。回滚不得恢复泄露/过期 secret，也不能放宽 audience/tenant/能力。
7. 信任域、授权服务器、协议、能力词表、TTL、审批、重放防护、复验频率和证据保留
   均为 `Pending`；P2.5-J 必须验证真实轮换、下一请求撤权、委托和最小资源权限。
8. 评审 [企业服务与工作负载身份设计](enterprise-workload-identity-design.md)。

P2.5-J 提供方适配、权威对账与 staging 验收设计（2026-08-31 提议）：

1. 固定成熟度：A～D 已实现，E 为验收设计，F～I 只有已批准设计。Keycloak 26.3.3
   仅证明 OIDC code flow 基线；当前不选择 IdP、不连接真实 tenant 或启用 production。
2. provider 差异仅进入 OIDC、Users/Groups directory、assurance/logout 和 workload
   credential adapters；tenant、role、session、知识空间和 grant 保持内部权威。
3. 设计 `identityreconciliation.Run` 深层模块：完整有版本的 Users 快照、双向漂移、
   dry-run/批准 apply、仅经 `Provisioner.Apply` 修复及独立完成证据。每项写入事务内
   CAS 校验 lease/fence、provider profile、policy 和计划摘要，失租 worker 不能提交。
4. 对账分页需 snapshot token/全局 revision，或两次全量遍历的稳定 ID+version canonical
   digest 相同；count 单独无效，partial/overage/限流失败不生成空集。撤权优先，
   tuple/tenant/tombstone 冲突隔离而不自动重绑。
5. staging driver 只调用公开 seam，不能直改数据库；隔离测试 tenant/身份/组/workload
   与生产分离，默认 dry-run，失败阻止晋级并记录清理结果，仓库不提交真实证据。
6. 组合矩阵覆盖 OIDC/SCIM lifecycle、权威对账、session、groups、emergency、workload/
   委托、故障/隐私/轮换/回滚；F～I 未实现项为 blocked，不能标 pass 或跳过。
7. 升级空证据模板为 2.0，绑定 provider/profile/connector/policy/commit/images、A～J
   结果、清理和八类固定失效触发器；对排除 signature envelope 的 RFC 8785 canonical
   payload 由每位责任人分别签名。每条风险接受绑定责任人与证据；任何 mandatory 非
   pass 或证据失效均阻止后续 production enablement PR。
8. 评审 [企业身份提供方适配与 Staging 验收设计](enterprise-identity-provider-staging-design.md)。

P2.5 企业决策登记与实现准入（2026-08-31 已批准计划）：

1. 汇总 P2.5-E～I 的全部 `Pending` 企业决策，为每项分配稳定 ID、必需责任人、
   建议基线、批准值/状态和私有证据引用；建议不得自动转为批准。
2. 定义 `Pending → Proposed → Approved/Rejected → Superseded` 状态与职责分离，
   provider、数值策略、真实责任人和凭据只能由有权企业责任人确认。
3. 建立按模块计算的实现准入：只有该切片依赖的决策全部有效批准，才允许开始实现；
   默认关闭、失败关闭和内部授权权威等安全不变量无需等待即可固化。
4. 先用登记文档完成逐行评审；本切片不选择 IdP、不创建凭据、不连接外部 tenant、
   不修改运行代码，也不声称真实 staging 或 production 已完成。
5. 后续独立实现登记 validator 和 CI 门禁；在此之前自动准入保持关闭，不声称文档
   状态已被机器强制。评审[企业身份决策登记与实现准入](enterprise-identity-decision-register.md)。

P2.5 个人演示身份策略与实现准入（2026-08-31 已批准计划）：

1. 建立 `personal-demo-v1`，由仓库所有者以 Demo Owner 身份批准 E～I 的 51 项本地
   实现值；使用 Keycloak 26.3.3、虚构 tenant/用户/组/workload 和 ignored secrets。
2. 状态使用独立 `DemoApproved`，不修改企业 51 项 `Pending` 或 D0～D7 `Blocked`；
   demo 结果不能进入企业 2.0 manifest，也不能解锁 staging/production。
3. PD0 只解锁 default-off 的本地代码和确定性测试；Keycloak realm、虚构资产、清理
   可重复且 secret 安全配置完成前，PD2 本地运行继续 blocked。
4. 将 P2.5-F1 定为首个 TDD 实现切片：会话数据模型与 provider-neutral 核心；先不
   接管现有 JWT/Cookie、OIDC 登录、生产配置或部署。
5. 评审[个人演示身份策略](personal-demo-identity-profile.md)，通过后另提 F1 代码 PR。

P2.5-F1 会话核心（2026-08-31 已合并）：

1. 新增 `0021_platform_sessions` migration，只保存 credential SHA-256、内部 user/tenant、
   认证方法/保证/时间、到期、generation、撤销、策略 revision 和最小 correlation；不
   保存 provider token/subject、密码、role 或 capabilities。
2. 通过 `session.Manager` 的 `Establish`、`Authenticate`、`Revoke` 深层 seam 实现
   随机不透明 credential、空闲/绝对到期、风险动作重新认证、原子轮换和撤销。
3. PostgreSQL adapter 使用 generation CAS 更新活动和轮换；读取/写入故障失败关闭，
   读取后并发撤销导致 CAS miss 时返回 deny，旧 credential 不得重放。主体撤销、新建和
   轮换串行锁定内部用户，并以 `revoked_before` 拒绝撤销前的旧认证证据，防止并发逃逸。
4. `SESSION_CORE_ENABLED` 默认关闭；仅 `ENVIRONMENT=dev` 与
   `IDENTITY_POLICY_PROFILE=personal-demo-v1` 可通过配置校验。本切片不在 main 构造
   模块、不接管 JWT/Cookie/OIDC，也不改变部署或企业 gate。
5. 以 public seam 的 red→green 测试覆盖建立/认证、到期边界、重新认证、逐会话/主体
   撤销、轮换重放、存储故障、并发 CAS、migration 与配置门禁；真实 PostgreSQL
   生命周期测试还验证摘要存储、轮换不延长绝对寿命、主体撤销、撤销 correlation
   持久化，以及并发建立无法越过主体撤销水位。

P2.5-F2 会话凭据验证（2026-08-31 已实现并本地验证，待 PR 评审）：

1. 在现有 HTTP `auth.Authenticator` seam 组合 JWT 与 session adapter；只以版本化
   `ps1_` 前缀选择 opaque 路径，不使用解析失败推断 credential 类型。
2. session adapter 去除传输前缀后调用 `session.Manager.Authenticate`；无效、到期、
   撤销、重新认证决策或存储故障全部拒绝，且永不回退 JWT。
3. allow 结果只用于内部 user/tenant 定位；每次请求从 `userstore` 校验用户存在、active
   和 tenant 一致，并按当前 role 重新计算 scopes，支持下一请求撤权。
4. Query API 只在 `SESSION_CORE_ENABLED=true`、`ENVIRONMENT=dev`、
   `IDENTITY_POLICY_PROFILE=personal-demo-v1` 同时满足时构造 PostgreSQL adapter；默认关闭
   时保持现有 JWT 行为。
5. 所有保护路由暂映射 standard `platform.request`；F2 不签发凭据，不改变登录、Cookie、
   OIDC、logout、部署或 production。风险动作和 reauthentication 事务进入 F3。

P2.5-F3a 风险判断与重新认证响应（2026-08-31 已实现并本地验证，待 PR 评审）：

1. 以 HTTP method + 路由模板把身份绑定、用户/角色/密码、tenant 创建、文档删除/发布、
   Agent 批准和 generation rollback 映射为独立 high-risk action；其他请求保持 standard。
2. `ps1_` 会话的 `demo-mfa` 认证时间达到 10 分钟边界时返回结构化
   `401 reauthentication_required`，不误报 forbidden，也不执行静默重定向。
3. reauthenticate 决策只携带内部 user/tenant/认证方法；adapter 在挑战前仍实时校验用户
   存在、active 和 tenant，session 模块不返回 role/capabilities 权限快照。
4. 无效、撤销、会话到期、用户撤权和存储故障仍返回普通 unauthorized；JWT 高风险请求
   在迁移期保持兼容，但不宣称具有 MFA 或 freshness 保证。
5. F3a 不解析 `acr`/`amr`/`auth_time`，不创建 state/nonce/PKCE 事务、不轮换 credential，
   不修改 Cookie、OIDC、logout、部署或 production；这些进入 F3b 及后续切片。

P2.5-F3b 可信认证证据与单次重新认证事务核心（2026-08-31 已实现并本地验证，待 PR 评审）：

1. `oidcauth.AuthenticateWithEvidence` 在提供方 adapter 内严格转换 Keycloak demo 声明：
   仅接受 `acr=2`、精确 `amr=[pwd,otp]` 和 10 分钟内的可信 `auth_time`，只向下游返回
   provider-neutral `demo-mfa` 证据；普通 OIDC 登录继续使用原 `Authenticate` 契约。
2. `StartReauthentication` 生成新的 state/nonce/PKCE，向 IdP 请求 `prompt=login`、
   `max_age=0`、`acr_values=2`，并把当前 credential 摘要、tenant、subject、策略 action
   和安全 return path 绑定到 TTL 有界事务；Redis/内存中均不保存 credential 明文。
3. `CompleteReauthentication` 一次性消费事务，恒定时间校验 state 与当前 credential，
   再核对解析后的内部 tenant/subject 和认证证据。任一绑定变化、弱/陈旧/含糊证据、
   普通/重认证事务混用、存储或 IdP 故障均失败关闭。
4. public seam 测试覆盖成功、重放、state/credential/主体漂移、普通事务混用、证据边界、
   Redis 跨实例消费以及存储/提供方不可用；原始 `acr`/`amr`/`auth_time` 不离开 adapter。
5. F3b 尚不连接 HTTP callback，不签发或轮换 `ps1_` credential，也不修改 Cookie、logout、
   Compose 或 production。F3c 再原子轮换会话并完成 Web/API 编排。

P2.5-F3c HTTP 重新认证与会话轮换（2026-08-31 已合并）：

1. Query API 新增重新认证 start/callback seam；start 只接受当前有效、联邦、版本化 `ps1_`
   会话，且 action 必须是已登记 high-risk 动作并由 session policy 判定需要重新认证。
2. callback 使用 F3b 的单次 state/nonce/PKCE transaction，重新核对当前 credential、内部
   tenant/subject、实时用户 authority 和可信 `demo-mfa` evidence，再通过
   `session.Manager.Establish(ReplacesCredential=...)` 原子轮换 credential。
3. Web BFF 只通过明确同源 POST 发起流程，使用独立 HttpOnly reauth-state Cookie；回调成功
   后才替换 `ai_etl_token`，失败只清理 state，不能删除、刷新或泄露原 session credential。
4. public HTTP/Cookie 测试覆盖 JWT/本地/新鲜会话、未知或 standard action、state/credential
   漂移、用户撤权、IdP/Redis/PostgreSQL 故障、并发 callback、重放、安全 return path，
   以及遗留 reauth Cookie 不劫持普通登录；Web 契约直接运行 production Next HTTP route。
5. F3c 不自动重放原高风险写请求，不实现 logout，不迁移普通 OIDC/password 登录签发，
   不修改 staging/production gate；这些能力继续拆为后续独立评审切片。

P2.5-F4 初始 `ps1_` 签发与个人演示登录迁移（2026-08-31 已实现并本地验证，待 PR 评审）：

1. 仅在现有 `SESSION_CORE_ENABLED=true`、`ENVIRONMENT=dev`、
   `IDENTITY_POLICY_PROFILE=personal-demo-v1` 三重门禁下，password 与 OIDC 成功登录改为
   调用同一 `session.Manager.Establish` seam 并返回版本化 `ps1_`；默认关闭仍签发 JWT。
2. session policy 区分“允许建立的认证保证”与“高风险所需保证”：password 只产生明确的
   `local-password`，可访问 standard 动作但不能伪装 `demo-mfa`；OIDC 只有严格验证
   `acr=2`、精确 `pwd+otp` `amr` 和新鲜 `auth_time` 后才能建立联邦 demo 会话。
3. 建立失败必须失败关闭，不回退 JWT，不返回 credential；实时 user active/tenant/role
   校验、一次性 OIDC transaction、审计脱敏与现有密码枚举防护继续保持。
4. Web password/OIDC route 只接受 `ps1_` 迁移响应，并按后端绝对到期设置
   Secure/HttpOnly/SameSite=Lax Cookie，`maxAge=min(30m, remaining absolute lifetime)`；
   失败不得写主 Cookie。
5. public HTTP/Cookie 测试覆盖两种成功登录、新凭据经过受保护 session route、默认关闭的
   JWT 兼容、弱/缺失 OIDC 证据、session/PostgreSQL 故障、OIDC 重放与 Cookie 属性。
   logout、最多 3 会话、会话列表/设备撤销及 staging/production 仍进入后续切片。

P2.5-F5 安全退出与当前会话撤销（2026-09-01 已实现并本地验证，待 PR 评审）：

1. Query API 在认证中间件外提供 `POST /v1/auth/logout`，按显式 `ps1_` 前缀调用既有
   `session.Manager.Revoke(RevokeCurrent)`；停用用户仍可撤销，存储失败返回 503，未知或
   已撤销 credential 幂等完成，绝不回退 JWT 认证。遗留 JWT 保留 Cookie-only 兼容退出。
2. logout correlation 由服务端生成且不含 credential；会话撤销和 correlation 在 session
   store 原子提交。安全审计不保存 bearer、provider token、原始 subject 或 logout state。
3. 联邦会话先完成本地撤销，再通过 OIDC adapter 可选生成经 discovery 验证的
   RP-initiated logout URL；独立 state 事务只允许消费一次。元数据、事务存储或 IdP
   不可用不能恢复本地会话，也不能阻止本地完成；不实现 back-channel logout。
4. Web `POST /api/auth/logout` 强制同源，把 HttpOnly credential 传给 Query API；后端失败
   保留主 Cookie，后端成功才以匹配属性和 `Max-Age=0` 清除。可选 provider state 使用
   独立 Secure/HttpOnly Cookie，callback 验证并消费后只回到安全本地路径。
5. public seam 测试覆盖 local/federated/JWT、停用用户、幂等/并发撤销、PostgreSQL/Redis/
   IdP 故障、CSRF、Cookie 成功/失败语义、安全 redirect 和旧 credential 重放。每用户最多
   3 会话、会话/设备列表、指定设备撤销及 staging/production 继续延期。

P2.5-F6 会话与设备管理（2026-09-01 已批准实施）：

1. 扩展 `session.Manager` 的 provider-neutral seam：每个会话分配独立随机管理句柄，列表
   只返回脱敏时间、认证方法和当前会话标志；浏览器不能看到数据库 ID、credential 摘要、
   provider subject/token、IP 或 User-Agent。
2. `personal-demo-v1` 通过 `SESSION_MAX_ACTIVE_SESSIONS` 配置每用户 1～3 个活跃会话，
   默认 3。超限建立时，在同一 PostgreSQL
   事务和用户锁内撤销最旧活跃会话、建立新会话并写入脱敏 `session_limit_eviction` 审计；
   会话轮换不占用新槽位，淘汰使用序列分配且轮换不改变的持久化 `creation_order`。
3. Query API 提供当前 `ps1_` 用户的 `GET /v1/auth/sessions` 与
   `DELETE /v1/auth/sessions/{handle}`。指定撤销必须再次校验当前 credential、tenant、user
   和目标所有权，原子写入 `session_device_revoked` 审计；跨用户/租户、伪造或已失效句柄
   统一幂等且不写审计，不泄露存在性。
4. Web BFF 只从 HttpOnly Cookie 转发 credential；DELETE 强制同源，响应和日志不得暴露
   credential。JWT 兼容模式明确返回不支持，不伪造设备列表或撤销能力。
5. 按 `session.Manager`、Query HTTP 和真实 Next HTTP/Cookie 三个公开 seam 逐个执行
   red→green。测试覆盖并发第 4 会话、确定性淘汰、轮换、当前标志、过期/撤销过滤、跨主体
   越权、幂等指定撤销、PostgreSQL/审计故障原子性、CSRF 和 Cookie-only 转发。
6. 本切片仍仅受既有 dev + `personal-demo-v1` + default-off 门禁保护；不实现管理员设备
   管理、IP/User-Agent 指纹、back-channel logout、staging 或 production enablement。

P2.3 backend projection slice (2026-08-28):

- Added generation-scoped Qdrant upsert/scroll and Elasticsearch upsert/scroll
  adapters behind the manifest projection seam. Physical IDs and payloads carry
  generation and durable document-version identity, plus content hashes.
- Added a cross-backend verifier that records both observations and marks a
  manifest ready only when each count/digest matches the expected set; backend
  errors and identity mismatches fail the manifest.
- Existing writes and pipeline completion behavior are intentionally unchanged
  until the next slice wires manifest creation and verification into processing.

P2.3 pipeline integration slice (2026-08-28):

- Added a deep generation-builder module that persists a deterministic unsealed
  manifest before backend writes, seals expected identity after parsing, runs
  dual-backend verification, and performs expected-current CAS activation.
- Persisted activation predecessors keep stale-writer protection effective
  across process crashes and delayed message redelivery.
- Durable outbox tasks now fail closed on any embedding, Qdrant, Elasticsearch,
  verification, or activation error. Their ingestion job reaches `completed`
  only after the generation is active; legacy messages keep the compatibility
  path and asynchronous Elasticsearch queue.
- Redelivery targets the same generation and idempotently rewrites all chunks;
  it no longer trusts legacy chunk-ID checkpoints. Build configuration changes
  deterministically create a different generation.
- This slice deferred background repair and retention, manifest metrics/alerts,
  and crash/reindex/rollback acceptance coverage. Reconciliation/repair is now
  implemented below.

P2.3 active-generation query slice (2026-08-28):

- Qdrant and Elasticsearch retrieval candidates now preserve durable document
  version and generation identity; Qdrant dense relevance and fusion keys are
  generation-scoped so repeated deterministic chunk IDs cannot collide.
- Query retrieval batches candidates through the PostgreSQL manifest read
  seam before fusion. Only an exact active identity is admitted; stale and all
  non-active states fail closed.
- Semantic-cache hits pass through the same gate on every read. Documents with
  no manifest retain legacy compatibility, while managed legacy points,
  malformed identities, and manifest lookup failures are rejected.
- This slice deferred reconciler/repair, rollback and retention cleanup,
  manifest metrics/alerts, and the remaining acceptance matrix.
  Reconciliation/repair is now implemented below.

P2.3 manifest reconciliation slice (2026-08-28):

- Added an enabled-by-default worker reconciler that fairly claims bounded
  batches of active manifests with PostgreSQL leases, fencing tokens, and
  `FOR UPDATE SKIP LOCKED` for safe multi-worker operation.
- Each claim independently observes Qdrant and Elasticsearch. Matching counts
  and identity digests persist a healthy result; backend errors or mismatches
  persist a durable diagnostic and make managed query evidence fail closed.
- Divergence atomically reopens the original ingestion job and outbox event so
  the existing relay and pipeline rebuild the exact deterministic generation.
  Already-pending work is not duplicated and consecutive repair scheduling is
  capped by configuration.
- A repair replay is not considered complete until both rebuilt projections
  are freshly observed and match the sealed manifest. Healthy confirmation
  clears the diagnostic and resets consecutive repair attempts.
- Real PostgreSQL tests cover concurrent activation, active/legacy visibility,
  a single pending replay, and healthy recovery.
- This slice deferred rollback and retention cleanup, bounded manifest
  metrics/alerts, and the remaining acceptance matrix. Rollback/retention is
  now implemented below.

P2.3 generation rollback and retention slice (2026-08-28):

- Added an administrator-only, tenant-scoped rollback operation. Requests must
  name the target and expected current generation; rollback verifies both
  projections before an advisory-locked PostgreSQL CAS swaps their states.
- A rollback target is leased and fenced during backend verification. Expired
  windows, partial backend data, stale active identities, cleanup-in-progress,
  and late claims fail closed.
- Added retired-generation cleanup with exact tenant/document/version/
  generation filters for Qdrant and Elasticsearch. Per-backend deletion state
  survives retries, and the manifest is removed only after both succeed.
- Rollback windows begin at retirement. Existing retired rows receive a fresh
  window during migration to prevent immediate cleanup after rollout.
- Cleanup is disabled by default. Enabling it requires an explicit positive
  `INDEX_RETENTION_WINDOW`; source objects and durable ingestion records are
  intentionally retained.
- The bounded metrics/alerts and final acceptance matrix are implemented below.

P2.3 generation observability and acceptance slice (2026-08-28):

- Added low-cardinality Prometheus snapshots for manifest state/age and durable
  divergence, repair-exhaustion, and retention-failure diagnostics. Lifecycle
  result counters use fixed outcomes only; tenant, document, and generation
  identities are never metric labels.
- Reconciliation, retention, and administrator rollback publish their results
  through optional observer seams. A worker monitor refreshes durable health
  from PostgreSQL independently of any single lifecycle pass.
- Added tested alerts for stalled builds, failed manifests, sustained backend
  divergence, exhausted repair attempts, and persistent retention failures.
- Added `docs/index-generation-acceptance.md` mapping crash replay, reindex,
  rollback, and garbage collection to automated evidence. Go CI now provisions
  PostgreSQL 16 so lifecycle integration tests cannot silently skip.
- P2.3 engineering is complete. Production enablement remains subject to an
  approved retention window and the deployment gates in ADR 0010.

## 2026-08-24 - P1.9 Business Gold Approval and Safety Refusal

Status: engineering implementation completed; business promotion remains
blocked on an external signed artifact from an authorized policy owner

Goal: close the open-ended P1.8 implementation search, provide a fail-closed path
from the private technical candidate to business-approved Gold, and make
permission-negative answers refuse when visible evidence is grounded but does
not answer the user's actual question.

Approved seams and acceptance criteria:

1. Close P1.8 without starting P1.8-D. Retain diagnostics and public benchmarks,
   but make no further cross-document production ranking changes.
2. At the Query Service `Ask` seam, require ambiguous-band answer verification
   to establish both evidence support and responsiveness. Either false verdict
   returns the canonical refusal with no sources or citations; supported,
   responsive answers retain current behavior.
3. At the Gold finalize CLI seam, bind approval to the exact candidate and an
   independently retained signed artifact by SHA-256. Require an authorized
   approver identity/role/time, complete document and case ID sets, and explicit
   document-authority, permission, effective-version, and answer attestations.
4. Missing artifacts, mismatched digests, incomplete scope, pending decisions,
   or false attestations must fail without writing output. Only a complete
   approval may create a private `enterprise_private_gold` dataset with
   `business_approval_complete=true` and auditable approval provenance.
5. Run focused and complete engineering verification. Screen the 15-case
   safety-negative cohort once; only a 100% valid result proceeds to three formal
   runs, each of which must retain 100% safety refusal.

Progress:

- The public Query Service test now reproduces a grounded but non-responsive
  answer using synthetic data. The verifier requires `supported=true` and
  `answers_question=true`; a false verdict uses the existing source-free refusal
  path without changing the Query API interface.
- Added a public-safe Gold approval template and a finalize CLI tested against
  missing signed evidence, digest mismatch, incomplete case scope, false
  attestations, and a fully approved synthetic candidate. Real private Gold
  remains `ready_for_business_approval` until an authorized owner supplies the
  signed artifact and completed manifest.
- Completed one real-model safety screen followed by three formal runs against
  the same private candidate digest, tenant, upload map, and configuration hash.
  Every run was valid with 15/15 successful queries, zero unavailable grounding
  checks, and 100% safety-refusal rate. Detailed reports remain local and Git
  ignored.
- Removed the dedicated P1.9 evaluation project's containers, volumes, network,
  and project-tagged images after the formal gate passed. The root 17-service
  development stack remained running with no unhealthy service.

Delivered:

1. P1.8 is closed without P1.8-D or additional ranking behavior.
2. Grounded but non-responsive ambiguous-band answers fail closed to the
   canonical source-free refusal; supported and responsive answers retain the
   existing API behavior.
3. The private Gold finalization path is implemented and verified, but no real
   candidate has been promoted because no authorized signed approval artifact
   has been supplied.
4. The safety refusal release gate passed three configuration-matched formal
   runs at 100%.

## 2026-08-23 - P1.8 Cross-Document Retrieval Diagnosis and Repair

Status: closed; diagnosis retained, automatic ranking implementation discontinued

Goal: identify the exact retrieval stage where a necessary source is lost, add
aggregate-only diagnostics and deterministic regression coverage, then apply the
smallest upstream repair before reconsidering reranking. Private case ids,
filenames, questions, answers, evidence, and detailed reports remain local and
Git ignored.

Approved plan and acceptance criteria:

1. Preserve the accumulated worktree and perform a read-only review; record
   logical checkpoint candidates without reverting or cleaning unknown changes.
2. Add tests and a public-safe diagnostic protocol that records only aggregate
   counts/booleans for necessary sources at backend candidate, fused Top-50, and
   final Top-5 stages. Do not expose private case identifiers or content.
3. Add deterministic retrieval/fusion regression coverage that constructs a
   multi-source query where one required document is lower-ranked, distinguishing
   backend recall, fusion truncation, deduplication/diversity, and final Top-K
   loss.
4. Use the failing tests and diagnostics to make the minimum candidate-fusion,
   multi-document retention, or diversity repair. Do not replace the reranker
   before the upstream loss is located; preserve permission, scope, exact-match,
   and grounding behavior.
5. Run focused tests, the full Go/Python/Web verification suites, and
   `git diff --check`.
6. In an isolated Compose project, repeat the no-rerank enterprise evaluation
   three times. Require cross-document all-required-source hit rate to be
   materially above 0% and ensure existing semantic, lexical, permission,
   grounding, and safety metrics do not regress.
7. Update aggregate-only evaluation documentation, this backlog, and
   `LEARNINGS.codex.md`; keep private artifacts untracked and do not create a
   mixed-scope commit.

Acceptance: stage-level diagnostics and deterministic regression tests identify
the loss boundary; the minimal repair passes focused and full verification; and
three valid no-rerank runs show stable, materially non-zero cross-document
all-required-source retrieval without regressions in existing gates.

Progress update:

- Added aggregate-only backend/fused/selected stage diagnostics, feature-gated
  at the Query API boundary, with focused retrieval/query/evaluator tests.
- Fixed isolated evaluator user provisioning when reusing an upload map and
  documented that upload maps do not carry Qdrant/Elasticsearch volumes.
- Added `--cohort cross_document` filtering that leaves the full document corpus
  intact, plus feature-gated `--retrieval-only` requests that skip answer LLM
  generation. Reports now show Qdrant and Elasticsearch stage rates separately
  instead of collapsing them to the maximum backend rate.
- Fixed retrieval-only evaluation to record the first successful response once
  instead of polling it repeatedly. Evaluator regression coverage now contains
  34 passing tests.
- Completed a valid 35-document/10-case isolated retrieval-only diagnosis. Query
  requests succeeded 10/10 with zero answer-model tokens. Qdrant and fused
  candidates each contained every necessary source in 40% of cases, while
  Elasticsearch and final Top-5 each did so in 0%.
- Fixed diagnostics to retain a successful backend that returns zero candidates;
  otherwise Elasticsearch disappeared from the stage report instead of being
  represented as a measured empty result.
- Tested a distinct-document-first selection hypothesis in a second valid run.
  It did not improve any stage rate, and every final result already contained
  five distinct documents, so that behavior change was rejected and reverted.
- Added aggregate-only `all_required_max_rank`: when a stage contains every
  necessary source it records the deepest first-occurrence rank; incomplete
  stages report zero. This permits the next isolated run to distinguish a
  shallow Top-K cutoff from deeper candidate-ranking failure without exposing
  document identifiers or content.
- A valid K=50 run found complete fused sources in 40% of cases, with deepest
  first-occurrence ranks 8, 35, 45, and 45. Raising candidate depth to 200 moved
  fused completeness to 80%, but added necessary sources at ranks 61, 91, 104,
  and 187; final Top-5 remained 0%. Candidate depth is therefore a diagnostic,
  not a sufficient product repair.
- Found two registry documents marked completed with zero chunks. One was a
  necessary cross-document source. Both parsers extracted non-empty text, but
  the noise filter discarded every chunk solely because long body text contained
  a table-of-contents keyword. Limited that heuristic to short fragments and
  made zero stored chunks a pipeline error instead of a false completion.
- Reprocessed only those two documents in the isolated stack. Qdrant unique
  document coverage increased from 33/35 to 35/35, and no necessary source was
  absent from the collection. A dense-only full-collection diagnostic then
  found all necessary sources in 10/10 cases, at deepest ranks from 8 to 216.
- Rejected after valid or offline evidence: distinct-document-first selection,
  sparse Chinese unigram and overlapping-bigram changes, MMR, and local 1.5B/4B
  multi-query rewriting. None improved final Top-5; the sparse changes regressed
  K=50 fused completeness from 40% to 10% and were reverted.
- Completed project verification: all Go tests and `go vet`, 32 parser tests,
  one lightweight reranker test, 102 script tests, Web lint/typecheck/production
  build, Compose configuration validation, and `git diff --check` passed. The
  root Compose project retained 17 running services.
- Removed 56 reviewed historical evaluation/test images and pruned only build
  cache older than seven days. The root development stack remains running.

Closure decision:

1. Keep the zero-chunk and conservative TOC-filter correctness fixes plus the
   aggregate diagnostics and public Query Planner benchmark.
2. Retain no P1.8-B or P1.8-C ranking behavior; do not start a P1.8-D document-
   enrichment implementation.
3. Treat automatic implicit cross-document planning as unsupported rather than
   extending the production retrieval path without a business-approved need and
   acceptance set.
4. Move active work to business Gold approval and the independent 100% safety-
   refusal gate in P1.9.

### P1.8-B Deterministic Multi-Semantic Retrieval

Status: rejected after a valid matched isolated comparison; no product ranking
change retained

Approved test-driven plan:

1. Keep the external retrieval interface unchanged. Add an engine-internal query
   plan that retains the original question and, only for semantic/hybrid routes,
   derives at most two meaningful clauses using deterministic punctuation and
   conjunction rules. Exact-keyword queries must remain single-path.
2. Add a Qdrant dense-only search mode for derived clauses. It must reuse the
   existing tenant, permission, knowledge-space, and applicable-scope filters,
   and must not duplicate sparse retrieval or the raw-cosine follow-up request.
3. Fuse the original whole-query ranking with clause rankings through a single
   coverage-aware module. Preserve the original top candidate, admit at most one
   leading candidate per derived clause before filling by aggregate rank, dedupe
   by chunk/document rules, and keep the final context limit unchanged at five.
4. Start with public-safe engine-interface tests: prove that a second semantic
   source absent from the original candidate window can enter Top-5 through its
   clause, while default-off, exact-query, authorization-filter, timeout, and
   deterministic-order behavior remain unchanged.
5. Add a disabled-by-default configuration switch and document the isolated
   evaluation opt-in. Do not expose evaluation-required document ids to query
   planning, backend requests, fusion, logs, or production configuration.
6. Run focused and complete Go verification plus the existing Python, scripts,
   Web, Compose, and diff checks. Retain the candidate only if those gates pass.
7. Then run an isolated retrieval-only comparison with no reranker. Require a
   material Top-5 cross-document improvement without semantic/lexical regression
   before enabling the strategy by default or starting three-run acceptance.

Acceptance: the public-safe benchmark promotes both independently relevant
semantic facets into an unchanged Top-5 without required-id access; existing
security and exact-query behavior pass unchanged; and a valid private aggregate
comparison improves cross-document Top-5 before any production-default change.

Outcome:

- Implemented the candidate test-first behind a disabled switch, with a small
  engine-internal interface, dense-only governed Qdrant clause searches,
  deterministic coverage-aware fusion, fallback behavior, and configuration
  provenance. Focused and complete engineering verification passed before the
  real comparison.
- Ran one feature-off baseline and one feature-on candidate against the same 35
  published documents, tenant, model, K=50, final Top-5, and no-rerank settings.
  Both runs were valid with 10/10 successful retrieval-only requests.
- Baseline and candidate were identical at the aggregate retrieval boundaries:
  Qdrant/Fuse all-required coverage remained 40%, and selected Top-5 remained
  0%. The candidate expanded 0/10 questions because none could be conservatively
  reduced to two complete semantic clauses.
- The private questions express cross-document needs implicitly rather than as
  two explicit punctuation/conjunction-delimited facets. Relaxing deterministic
  splitting would guess missing intent and risk semantic/lexical regressions, so
  the candidate failed its gate and all implementation/configuration changes
  were reverted.
- Detailed reports remain Git ignored. The dedicated Compose project, volumes,
  network, and five project-built images were removed; the root stack remained
  running.

Next design gate: evaluate document-side semantic units or a constrained local
query planner that can infer implicit facets without runtime required ids. Any
new candidate needs a public-safe activation/coverage benchmark before another
private run; punctuation splitting must not be reintroduced as the main path.

### P1.8-C Constrained Local Query Planner

Status: completed; public gate failed and the runtime candidate was reverted

Approved test-driven plan and predeclared gates:

1. Keep the external `retrieval.Engine.Retrieve` interface unchanged. Place the
   planner seam inside the retrieval module, with one production HTTP adapter
   for loopback Ollama and deterministic in-memory adapters at the engine test
   seam.
2. Add a tracked, public-safe benchmark containing cross-document implicit-facet,
   single-semantic, and lexical/exact cohorts. Run it three times before any
   private evaluation. Require 100% valid structured responses, at least 80%
   cross-document activation, at least 80% expected-facet coverage, no lexical
   or exact-query activation, and at most 10% activation on single-semantic
   controls.
3. Accept only a strict JSON object with an explicit activation decision and two
   or three bounded, distinct facet queries. Reject markdown, unknown fields,
   blank/duplicate facets, copied whole questions, and output that introduces
   exact identifiers absent from the input. Invalid output, timeout, planner
   error, or a non-local endpoint must fail closed to the original retrieval
   path.
4. Never plan strong exact-token queries. For eligible semantic/hybrid queries,
   search the original query plus validated facets under the same tenant,
   permissions, knowledge-space, applicable-scope, timeout, and candidate
   limits. Do not pass evaluation-required document ids to planning or ranking.
5. Fuse query rankings deterministically behind the retrieval interface, dedupe
   chunks, keep final Top-K unchanged, and expose only aggregate planner
   activation/facet counts in retrieval provenance. Preserve existing cache,
   rerank, relevance, governance, grounding, and authorization behavior.
6. Add vertical-slice tests at the benchmark and engine seams for default-off,
   multi-source promotion, exact-query bypass, scope propagation, malformed
   output, timeout/error fallback, deterministic order, and provenance privacy.
7. Only after the public gate passes, run one matched isolated retrieval-only
   baseline/candidate comparison with no reranker. Reject and revert if selected
   Top-5 cross-document coverage does not improve materially or semantic/lexical
   controls regress. A promising candidate then proceeds to the existing
   three-run no-rerank acceptance gate.
8. Run complete Go/Python/scripts/Web/Compose verification and update aggregate-
   only evaluation documentation plus `LEARNINGS.codex.md`. Keep private inputs,
   identifiers, questions, evidence, and detailed reports Git ignored.

Acceptance: the public benchmark passes every predeclared activation and
coverage gate; engine-interface tests show both implicit facets entering the
unchanged Top-5 without weakening scope or exact-query behavior; and no private
evaluation begins until those conditions hold.

Outcome:

- Added an 18-case public-safe benchmark with 8 implicit cross-document cases,
  6 single-semantic controls, and 4 lexical/exact controls. Its parser and gate
  tests pass, and detailed local model reports remain Git ignored.
- `qwen2.5:1.5b` first produced no valid strict responses. After a generic
  schema-consistency prompt correction it produced 100% valid responses, but
  activated every cohort and covered only 6.25% of expected facets: cross-
  document activation 100%, semantic false activation 100%, and lexical
  activation 100%.
- The already-installed `qwen3:4b` failed the one-run screen with 0% valid
  responses, 0% cross-document activation, and 0% expected-facet coverage.
  The formal three-run gate was therefore not started.
- The test-first runtime candidate had covered local-only access, strict output,
  exact-query bypass, governed scope propagation, deterministic fusion, fallback,
  and aggregate-only provenance. Because no model passed the public gate, all
  runtime/configuration behavior and candidate Go tests were reverted. No private
  enterprise evaluation was run and the production retrieval path is unchanged.
- Complete verification passed: 105 script tests, Go formatting/vet/full/race
  tests, 32 parser tests, one lexical reranker test, Web audit/lint/typecheck/
  production build, both Compose configurations, and diff/residual checks. The
  two dedicated Go cache volumes were removed; all 17 root services remained
  running without an unhealthy service.

## 2026-08-21 - P1.7 Enterprise Reranker Repeated A/B Evaluation

Status: completed; `auto` rerank failed the predeclared release gates

Goal: compare no-rerank and `auto` rerank on the same private enterprise
candidate dataset with auditable configuration, cohort-level metrics, and at
least three matched repetitions per arm before changing the production default.
Private filenames, questions, answers, evidence, and case-level reports must
remain local and Git ignored.

Approved plan and acceptance criteria:

1. Add public-boundary tests for the evaluator JSON/Markdown reports before
   adding cohort metrics, request latency, and resolved configuration
   provenance. Preserve legacy dataset and report behavior.
2. Record each case's `evaluation_cohort`; summarize lexical, semantic,
   cross-document, and safety-negative cohorts separately. Include Recall@5,
   all-required-document hit rate, citation completeness, key-fact coverage,
   safety refusal, grounding, p50/p95 final Query API request latency, and token
   usage with explicit eligible-case denominators.
3. Record the dataset SHA-256, embedding/LLM/reranker models, rerank enable and
   policy, candidate/final Top-K, query Top-K, grounding configuration, and
   other non-secret settings needed to detect configuration drift.
4. Add CLI-boundary tests, then extend `analyze-eval-variance.py` to compare
   baseline and candidate groups while retaining same-arm variance analysis.
   Report cohort mean, sample standard deviation, range, case flips, and the
   observed within-arm noise floor; do not present this floor as a formal
   hypothesis test.
5. Use one dataset digest, tenant, upload mapping, corpus, Top-K, embedding, LLM,
   and reranker model for all runs. Run no-rerank at least three times, then
   `RETRIEVAL_ENABLE_RERANK=true` with policy `auto` at least three times in an
   isolated Compose project. Do not reuse, stop, or mutate the root development
   stack.
6. Require semantic Recall@5 improvement of at least the larger of 3.33
   percentage points or observed noise, and cross-document all-required-source
   hit-rate improvement of at least the larger of 10 percentage points or
   observed noise.
7. Reject the candidate if lexical Recall@5, citation completeness, key-fact
   coverage, or grounding regresses beyond observed noise; require 100% safety
   refusal in every run. Limit p95 latency increase to both 50% and two seconds,
   and average token increase to 20%.
8. Publish only aggregate, non-sensitive P1.7 results and limitations. Keep the
   technical candidate distinct from business-approved acceptance Gold, update
   evaluator documentation and the learning log, and run focused plus complete
   verification before completion.

Delivered:

- Added tested cohort-level JSON/Markdown summaries, Query API latency and
  completeness fields, dataset/configuration provenance, and invalid-run gates.
- Added matched 3+3 A/B analysis with configuration-drift checks, per-metric
  means, sample standard deviations, ranges, noise floors, aggregate case flips,
  and the approved release thresholds.
- Made the Query API handler timeout configurable while preserving its 60-second
  default; the isolated evaluation used a seven-minute handler/write timeout so
  the local LLM could complete without middleware cancellation.
- Completed three valid no-rerank and three valid `auto` runs: all six had 70/70
  successful Query API cases, zero unavailable grounding checks, one dataset
  digest, and stable within-arm configuration hashes.
- Rejected `auto`: semantic Recall@5 changed from 68.89% to 33.33%, lexical from
  66.67% to 35.56%, cross-document all-required-doc hit rate remained 0%, and
  candidate safety refusal was not 100% in any run. The default remains
  no-rerank. Aggregate details are in
  `docs/evals/p1.7-enterprise-reranker-ab.md`.
- Kept all private case data and detailed reports Git ignored and local; retained
  `business_approval_complete=false`.

## 2026-08-21 - P1.6 Semantic and Cross-Document Evaluation Expansion

Status: completed; technical candidate v2 generated and validated

Goal: expand the private enterprise candidate set into distinct lexical,
semantic, cross-document, and safety cohorts before making any reranker choice.
All generation and review must remain local, preserve verbatim source evidence,
and keep the result explicitly below business-approved acceptance gold.

Approved plan and acceptance criteria:

1. Add tests for natural-query rewrite validation, evidence binding,
   cross-document compatibility, deterministic identifiers, cohort accounting,
   and fail-closed local review behavior.
2. Rework technically usable rejected cases into natural paraphrases with lower
   source overlap while retaining complete reference answers and exact evidence.
3. Generate controlled cross-document cases only from documents in compatible
   business scopes; require evidence and expected document references for every
   source used by an answer.
4. Run mechanical validation and an independent local-model review. Never send
   private filenames, text, questions, answers, or evidence to an external
   endpoint.
5. Produce private `gold-candidate-v2.json` with explicit cohort metadata and
   at least 30 semantic, 15 lexical, 10 cross-document, and 15 safety-negative
   cases. Keep cohorts separate in reports and do not call this acceptance gold.
6. Validate schema, document references, evidence integrity, cohort counts, and
   relevant script tests; publish aggregate-only results and limitations.

The subsequent reranker experiment is a separate gate: compare matched
no-rerank and `auto` runs with at least three repetitions only after this dataset
passes P1.6 validation.

Delivered:

- Added a tested, cached local pipeline using `qwen3:4b` for generation and
  `qwen3:1.7b` for independent review; non-loopback model endpoints fail closed.
- Produced a private 35-document/70-case `gold-candidate-v2.json` with 30
  semantic, 15 lexical, 10 cross-document, and 15 safety-negative cases.
- Rejected 57 of 86 semantic generation attempts mechanically before review;
  retained 29 new semantic cases plus the existing reviewed semantic case.
- Bound all 40 semantic and cross-document cases to private verbatim evidence;
  all source hashes, case references, key facts, and cohort counts validated.
- Extended the evaluator so cross-document cases must retrieve and cite every
  `required_doc_ids` source. Legacy cases retain their previous behavior.
- Kept `business_approval_complete=false`; detailed data and caches remain Git
  ignored. Aggregate results are in `docs/evals/p1.6-semantic-cross-document.md`.

## 2026-08-20 - P1.5 Automated Business Review and Gold Draft

Status: date-rule confirmations completed; gold candidate generated

Goal: perform the evidence-based portion of enterprise dataset review locally,
reduce manual work to genuinely authoritative business decisions, and produce a
private gold draft without presenting model inference as business approval.

Plan:

1. Verify every source path and SHA-256 before reviewing any derived label.
2. Apply conservative permission decisions from sensitivity and PII signals;
   exclude low-content documents and unresolved version conflicts from the gold
   draft.
3. Use a local model to suggest business domain, document kind, effectiveness
   signals, and independently review question/answer/evidence consistency.
4. Promote only technically verified cases whose documents do not require a
   blocking authority decision. Mark the output as a technical gold draft, not
   business-approved acceptance gold.
5. Generate a minimal private confirmation list for effective-version and
   ownership decisions that cannot be established from document evidence.
6. Validate the draft, publish aggregate-only results, and keep all private
   review details Git-ignored and local.

Delivered:

- Verified source paths and SHA-256 for all 40 documents before label review.
- Ran a cached local `qwen2.5:1.5b` review with structured, non-narrative output;
  no private content was transferred externally.
- Technically admitted 27 documents, excluded 4 low-content documents, held 2
  documents for one unresolved version conflict, and routed 7 current-effect
  decisions to business authority.
- Mechanically verified all 111 positive evidence bindings. Independent semantic
  review retained 16 positive cases and all 17 safety negatives; failures were
  concentrated in unnatural questions, incomplete answers, and weak support.
- Produced a private 27-document/20-case technical gold draft with separate
  lexical, semantic, and safety cohorts, initially leaving 8 confirmation rows.
- Applied the user-approved latest-date rule to all 8 confirmations. Seven
  dated historical items were marked `historical`; the version conflict kept
  the document with the later embedded Office modification date.
- Generated a private 35-document/31-case `gold-candidate.json`. All date-rule
  confirmations are resolved, while `business_approval_complete=false` remains
  explicit because recency is not a substitute for a policy register.

Details and limitations are recorded in `docs/evals/p1.5-automated-review.md`.

## 2026-08-20 - P1.4 Enterprise Chinese Silver Evaluation

Status: completed for local auto-silver baseline; business gold review pending

Goal: establish a local-only enterprise evaluation baseline from all 40 real
documents in `rag_datas/`, including the 12 sensitive candidates, without
sending their contents to an external model endpoint.

Plan:

1. Exclude `rag_datas/` and generated private artifacts from Git and retain only
   aggregate, non-sensitive results in tracked documentation.
2. Add tested XLS, XLSX, and PPTX support across upload validation, parser
   routing, and worker dispatch; verify OCR fallback for scanned PDFs.
3. Build a deterministic inventory and local extraction pipeline with document
   hashes, draft permission labels, provenance, evidence spans, and PII flags.
4. Use a local Chinese model to draft questions and reference answers. Accept
   only cases whose cited evidence is present verbatim; route conflicts,
   permissions, weak OCR, and low-confidence cases to a private review table.
5. Validate at least 100 silver cases across factual, procedural, cross-document,
   refusal, permission, version-conflict, and citation cohorts.
6. Run matched local retrieval baselines, publish aggregate metrics and misses,
   and keep the silver set distinct from business-approved gold acceptance data.

Delivered:

- Parsed all 40 documents and generated 114 locally validated silver cases:
  109 positive and 5 no-answer, including all sensitive candidates.
- Kept source files, extracted text, review tables, generated cases, and detailed
  reports in Git-ignored local directories; no private content was sent to an
  external LLM or Judge.
- Completed a real `bge-m3` + `qwen2.5:1.5b` baseline without reranking. Recall@5
  was 83.49%, negative pass rate was 100%, answer pass rate was 26.32%, and
  end-to-end pass rate was 24.56%.
- Classified failures without publishing private values: 18 retrieval misses,
  27 missing citations after a hit, and 41 missing key facts after a hit.
- Added original-binary eval uploads, source hash validation, local model limits,
  query-aware context selection, ETL readiness polling, upload-map reuse, and
  isolated Compose disk/port controls needed for a reproducible private run.

Remaining acceptance work: data owners must review permissions, effective
versions, conflicts, PII flags, and low-confidence cases before any sample is
promoted from silver to gold. The baseline details and next gates are documented
in `docs/evals/p1.4-enterprise-baseline.md`.

## 2026-08-19 - P1.3 Real Model Retrieval Baseline

Status: completed for public retrieval baseline; enterprise acceptance dataset still pending

Goal: measure retrieval quality with the configured real embedding/LLM stack and
compare reranking without presenting a sampled public result as enterprise proof.

Delivered:

- Verified the active stack: `bge-m3` 1024-dimensional embeddings and
  `deepseek-v4-flash` through the configured external OpenAI-compatible endpoint.
- Ran the same deterministic 30-document/10-query NanoSciFact sample with and
  without reranking. No-rerank Recall@5 was 70%; rerank `auto` Recall@5 was 90%.
- Recorded model mode, provenance, sampling, token counts, and result reports in
  `docs/evals/p1.3-real-model-baseline.md` and ignored report directories.
- Fixed an evaluator race where ES count was ready before ETL task completion;
  publication now waits for every `/v1/tasks/{doc_id}` to reach `completed`.
- Completed the full 2,919-document/50-query run with real `bge-m3` +
  `deepseek-v4-flash`, both without reranking and with `auto` reranking.
- Added batch processing timeout, immediate 429 scan backoff, and a
  dataset-digest-bound upload map so matched reranker runs reuse the same
  published corpus instead of re-embedding it.

Findings:

- No reranker: Recall@1/3/5 = 52%/60%/66%.
- Reranker `auto`: Recall@1/3/5 = 54%/72%/72%; 4 misses recovered and 1 hit
  regressed. Token usage increased from 92,617 to 105,721.
- Public benchmark metrics are directional only; do not use them as an
  enterprise release gate. The next gate requires reviewed Chinese enterprise
  data and repeated-run confidence intervals.

## 2026-08-19 - P1.2 Public RAG Benchmark Baseline

Status: implemented and verified (P1)

Goal: establish a reproducible public retrieval baseline without presenting
public benchmark scores as proof of enterprise-domain answer quality.

Approved seams and acceptance criteria:

1. A command-line importer converts standard BEIR corpus, query, and qrel files
   into evaluation protocol v2. Documents are uploaded once even when multiple
   queries reference them, and each query retains all relevant document ids.
2. Every import records dataset name, immutable version, source URL, SPDX-style
   license, split, sampling rules, and whether the result remains comparable to
   the full benchmark. Imports without explicit license provenance fail closed.
3. A reviewed public dataset catalog exposes only approved datasets. The first
   integration is NanoSciFact (`CC-BY-4.0`) for retrieval evaluation; it does
   not claim Chinese or enterprise-domain coverage.
4. The existing evaluator accepts both legacy case-per-document files and v2
   document/query datasets. Reports clearly identify public/private/synthetic
   origin, retrieval-only scope, sampling, and model mode.
5. Tests exercise CLI output, invalid provenance, deterministic sampling,
   evaluator compatibility, and report disclosure. Network tests mock only the
   external dataset API boundary; CI never depends on live Hugging Face access.

Definition of done: the approved NanoSciFact snapshot can be imported and
validated locally, deterministic tests and the existing eval suite pass, and
documentation gives separate commands for quick sampled checks and a complete
real-model baseline.

Delivered and verified:

- Added protocol v2 with separate documents and queries, unique-document upload,
  multi-relevance qrels, retrieval-only behavior, and provenance-rich reports.
- Added an approved public dataset catalog and a paginated Dataset Server
  downloader pinned by commit, row count, license, and SHA-256 content hashes.
- Downloaded, imported, and strictly validated the 2,919-document, 50-query,
  56-qrel NanoSciFact snapshot in Git-ignored local storage.
- Added deterministic sampling with explicit non-comparability and documented
  separate mock smoke and complete real-model commands.
- Passed 38 script tests, a protocol-v2 30-document/3-query isolated smoke, and
  the legacy 47-case isolated regression at 100% across retrieval, answer,
  overall, and negative-security gates. Mock scores remain non-quality signals.

## 2026-08-19 - P1.1 Evidence Sufficiency and Safe Refusal

Status: implemented and verified

Goal: prevent a semantically similar but identifier-mismatched candidate from
reaching generation, and make every evidence refusal canonical and source-free.

Delivered and verified:

1. Strong business identifiers such as contract numbers, order ids, trace ids,
   UUIDs, emails, and phone numbers require a surviving authorized candidate
   containing the requested identifier before the LLM is called.
2. Common model refusal variants are normalized to the canonical refusal and
   returned with empty retrieved sources and citations.
3. Retrieval diagnostics expose whether exact evidence was required and whether
   it matched; the Workbench renders this as `强标识校验` without exposing tokens.
4. Deterministic eval now has an independent `negative_pass_rate` gate that
   defaults to 100% and is explicit in CI.
5. The 47-case isolated eval passed at 100% for retrieval, answers, overall, and
   security negatives. All three confidential negative cases returned the fixed
   refusal with zero sources.

## 2026-08-19 - Web Framework Security Upgrade

Status: implemented, verified, and deployed locally (P1)

Goal: remove the high-severity Next.js, PostCSS, and Sharp advisories reported by
`npm audit` without regressing authentication or API proxy routes.

Plan:

1. Upgrade deliberately to Next.js 16.3.1 on Node 20 while retaining compatible
   React 18. The maintained Next.js 15 backport still selects a Sharp release
   affected by current high-severity advisories, so it cannot meet the gate.
2. Migrate deprecated metadata/viewport configuration and resolve build warnings.
3. Re-run login/session, upload, document search, knowledge-space, and query SSE
   browser-boundary tests plus the production build and dependency audit.
4. Require zero known high/critical production dependency findings before merge.

Delivered and verified:

- Upgraded to Next.js 16.3.1 on Node 20 with patched PostCSS 8.5 and Sharp 0.35,
  migrated async route parameters and viewport metadata, and added ESLint 9.
- Added a Web `.dockerignore` so local dependencies, build output, npm settings,
  and environment files never enter the image build context; disabled telemetry.
- Regenerated the lock file against the official npm registry. Clean `npm ci`,
  full `npm audit`, ESLint, TypeScript, and the production build all passed with
  zero vulnerabilities and no framework warnings. CI now runs these checks in a
  dedicated Web job included in the repository's `Required Checks` gate.
- The isolated upload/login/session/document-proxy smoke passed. The deployed
  Web also passed knowledge-space, `办公用品` search, and query SSE proxy checks.

## 2026-08-19 - Enterprise Knowledge Governance P0

Status: implemented and deployed locally

Goal: make knowledge selection an explicit, authorized product decision instead
of a free-form metadata convention or a consequence of retrieval rank.

Approved vertical slices and public test seams:

1. Add first-class tenant knowledge spaces, membership roles, default production
   spaces, document publication state, and deterministic legacy backfill.
2. Add a deep `knowledgecatalog` module whose interface resolves query scope,
   authorizes upload destinations, lists accessible spaces, and filters evidence.
3. Resolve the space before Qdrant/Elasticsearch retrieval; remove Top-1 scope
   selection and fail closed when authoritative governance cannot be checked.
4. Accept `knowledge_space_id` in upload/query, expose space management routes,
   and return the resolved space in retrieval diagnostics.
5. Add workbench and upload selectors plus document space/publication display.
6. Quarantine demo content from default production retrieval while retaining
   legacy scope metadata for controlled rollback.
7. Verify through catalog, migration, handler, full-stack isolation, full test,
   lint, build, Compose, and deterministic evaluation seams.

Definition of done: no unauthorized, cross-space, draft, superseded, archived,
or demo-by-default evidence reaches generation; existing production documents
remain queryable after backfill; catalog failures do not produce an answer.

Delivered and verified:

- Added tenant knowledge spaces, memberships, document publication states, and
  deterministic legacy backfill. Migration `0006` also provisions the default
  space transactionally for every tenant created after schema migration.
- Query and upload now resolve and authorize an explicit space before retrieval
  or ingestion. Evidence is checked against authoritative published document
  state, and catalog failures fail closed.
- Added space APIs, document publication controls, dynamic Web selectors, and
  space/publication diagnostics. Demo content is quarantined from the default
  production space.
- The live stack, fresh-volume E2E, full Go/Python/Web suites, Compose checks,
  and deterministic 47-case eval passed. The mock eval achieved 100% retrieval
  assertions and 93.62% answer assertions; its three answer failures were the
  mock's non-canonical refusal wording, with no confidential target retrieved.

## 2026-08-19 - RAG evidence integrity and truthful retrieval diagnostics (implemented)

Status: implemented and deployed locally

The live query `办公用品` exposed a chained correctness failure rather than a
single prompt issue: one legacy Word upload produced repeated chunks, retrieval
allowed one document to consume four of five context slots, documents from
different operational scopes were synthesized together, and the SSE workbench
skipped the grounding behavior implemented by the JSON query path. Retrieval
diagnostics also reported the selected context count as "candidates" and could
replace the raw Qdrant cosine with an RRF score depending on map iteration order.

Approved vertical slices and public test seams:

1. Preserve raw Qdrant relevance through cross-backend fusion and make the result
   independent of backend iteration order (`retrieval.Fuse`).
2. Make JSON and SSE `/v1/query` share retrieval, relevance filtering,
   governance, refusal, and grounding behavior.
3. Remove normalized duplicate chunks at parse time and diversify final Top-K by
   content and document, without preventing legitimate multi-chunk answers.
4. Replace single-character OR behavior for Chinese keyword search with a
   phrase-first query and versioned CJK index mapping.
5. Separate knowledge spaces, carry source metadata into citations, and prevent
   unqualified synthesis across incompatible scopes.
6. Expose backend, fused, deduplicated, selected-context, and unique-document
   counts separately; distinguish retrieved evidence from answer citations.
7. Reprocess the affected document, rebuild the local text index, flush semantic
   cache, and verify `办公用品` through the browser-facing SSE endpoint.

Definition of done: no exact duplicate content in Top-5, at most two chunks per
document when alternatives exist, stable cosine diagnostics, semantically
equivalent JSON/SSE safety outcomes, scoped answers with traceable citations,
and focused plus full-project verification passing.

Delivered and verified:

- Parser output now removes NFKC/whitespace-equivalent chunks and reindexes IDs.
  The affected legacy Word file dropped 10 duplicate chunks (18 to 8).
- Retrieval preserves Qdrant cosine, collapses duplicate files by `file_hash`,
  removes duplicate content, and enforces two chunks per document when distinct
  alternatives exist.
- SSE reuses the JSON `Ask` path, so relevance gating, governance, refusal, and
  grounding checks cannot diverge or leak provisional answers.
- Elasticsearch uses phrase-first plus AND fallback and a versioned CJK index;
  71 documents were migrated atomically to the `documents_text` alias.
- Upload/query contracts carry `knowledge_base_id` and `applicable_scope`.
  Custom scope writes require admin; unqualified queries select one scope and
  disclose cross-scope filtering instead of synthesizing incompatible sources.
- Responses distinguish retrieved evidence from answer citations, enrich cited
  chunks with file/scope metadata, and expose backend/fused/deduplicated/final
  counts. The Workbench renders these values and verifier-unavailable state.
- Browser-facing verification for `办公用品`: document search HTTP 200; automatic
  scope selected one demo source and filtered four cross-scope candidates;
  explicit user-upload scope returned five unique chunks from one canonical
  file, one citation, and a passing grounding verdict.

## 2026-08-17 - 语料治理：写入分级、文档身份、去重、作废与冲突披露（已交付）

Status: implemented

此前治理集中在「读」侧（检索源头权限过滤），「写」侧几乎没有门禁，四个缺口均经读码确认：

1. **机密写入门禁**：`normalizePermission` 原先只校验值在白名单内、不看调用者 role，user 可上传 `permission=confidential`（写进去自己反而读不到）。新增 `canWritePermission`，复用 `query.AllowedPermissionsForRole` 同一套矩阵——写入范围与读取范围一致，非成员 403 并埋审计。
2. **doc_id 所有权校验 + 先写后删**：用户自选 doc_id 时会 `cascadeDeleteDoc` 删除旧文档的向量/全文/对象且无所有权校验，同租户任何 user 可覆盖删除他人（含 admin 的机密）文档。现在替换前先 `Get` 现有行，校验密级可写 + `uploaded_by`（admin 不受限），任一不过直接 403 且不碰任何后端；并把清理改到「对象已写 + Kafka 已投」之后（`cascadeDeleteDocExcept` 放过新对象），避免后续失败导致两个版本都不在。
3. **file_hash 去重**：`file_hash` 原先只写不读。新增 `GetByHash` + `documents_tenant_hash_idx`（非唯一，同内容合法地存在于多个 doc_id）。未指定 doc_id 时命中已完成的同内容文档即返回 200 + `duplicate_of`，不重复入库——重复副本会挤占 Top-K 候选位，直接拖累检索质量。指定 doc_id 是明确替换意图，不查重。
4. **受控文件字段 + 作废过滤 + 冲突披露**：migration 0004 加 `doc_status`/`effective_date`/`supersedes`/`owner`（`doc_status` 是知识源生命周期，与 ETL 的 `status` 是两个维度）。**作废过滤在检索后置阶段按 `documents` 表做**，因为 chunk payload 只在 ETL 写入时生成、代码中不存在任何 `set_payload`，把已入库文档标作废不会回写其 chunk。冲突判据保守可解释、不引入 LLM 判断：证据集内出现 `supersedes` 链两端同时被引用，或同 `file_name` 但 `effective_date` 不同。前端在回答上方披露双方 doc_id/生效日期，交由人裁决。
5. **UI 收敛**：数据接入撤掉 doc_id 输入框（编号由系统分配），非 admin 不显示 confidential 选项；更新文档改为文档详情页「上传新版本」+ 二次确认。注册表 API 与详情页透出治理字段。

设计取舍：注册表故障在去重与作废过滤两处都降级放行（记日志继续），因为二者都不是机密性控制——机密性由检索源头的权限过滤保证，不受影响。未登记的 doc_id 视作 active（reconciliation 会补行，丢弃反而静默缩小证据集）。

范围外（enterprise 做法，尚未实现）：

- 按目录/空间继承密级（SharePoint/Confluence 模式，需新增 spaces 数据模型）
- 自动分类器（内容检测敏感信息自动升密级）
- 近似重复检测（MinHash/SimHash；`file_hash` 只能抓精确重复，改一个字就绕过）
- 发布审批流（受控文件进语料前的门禁）
- 复审周期与「未验证」标记（Guru 模式：过期知识主动降权/提示）
- 治理字段的 UI 入口：`/v1/upload` 已接受 `doc_status`/`effective_date`/`supersedes`/`owner`（admin-only，非 admin 传即 400 而非静默忽略；空值表示「未提供」，普通重传不会清空既有标记），`load-corpus.py` 会透传语料里的这些字段。仍缺前端：文档详情页无治理字段编辑，把一份文档标作废目前只能走 API 或 SQL
- 治理字段的独立更新接口：现在只能随上传附带，改一个 `owner` 也要重传文件。缺 `PATCH /v1/documents/{id}` 之类的纯元数据写入口
- 冲突检测只认显式关系（`supersedes` 链、同 `file_name` 不同 `effective_date`）。实测查「住宿限额」时 `HR-2024-005`（差旅报销流程）也写着五百元，与 `FIN-2025-001` 的八百元矛盾，但两者无 `supersedes` 关系、文件名也不同，因此 `conflict_detected` 不置位——这次是 LLM 自己在答案里提示了不一致，属运气而非机制。要覆盖这类跨文档矛盾需主题聚类或数值抽取比对，比现有保守判据重得多，故留作后续
- `documents.chunks_done` 恒为 0（45 行全部如此，与本次改动无关）：向量确实在 Qdrant（每篇 1 chunk），只是计数器没回写，前端进度显示因此不可用
- 回归脚本把 `api500` 计入检索未命中：实测 `HR-2024-005` 报 miss，根因是上游 LLM 返回空响应（`empty LLM response`，90 分钟内仅 1 次，重试 4 次全部命中 Top-1），检索链路正常。生成失败与检索未命中混在一个指标里，会让上游抖动看起来像检索退化，应分开统计

## 2026-08-16 - 系统真实感改造：知境（已交付）

Status: implemented

面试演示系统的"demo 感"（空文档 + 自指产品语料 + 泛化品牌）改为真实企业在用的 RAG 系统：

1. **真实业务语料**：`docs/corpora/enterprise-kb.json` ——「中科智远科技有限公司」企业知识库 44 篇（internal 33 / confidential 4 / public 7），覆盖人力资源（员工手册/年假/考勤/报销/招聘/绩效/薪酬保密）、财务（预算/差旅标准/审批权限/经营目标）、法务合同（审批/签署权限/保密协议/知识产权）、采购（办法/招标/供应商）、项目管理（立项/里程碑/风险/验收）、信息安全（密码/数据分级/应急）、行政、产品技术（发布/选型/代码评审/API/路线图）。doc_id 编号风格（HR-2024-003）、content 真实制度风格（条款/金额/天数/流程）。经 `load-corpus.py --source` 灌入主栈（admin，default 租户）。
2. **品牌化「知境」**：`web/app/layout.tsx`、`web/app/login/page.tsx`、`web/components/AppShell.tsx` 由「AI-ETL 企业知识库」改为「知境 · 企业知识库 / 企业智能知识平台」。
3. **权限隔离演示**：`zhangwei`（普通员工，user 角色）可见 40 篇（public+internal，无 confidential）；confidential 4 篇（薪酬/经营目标/合同签署/产品路线图）仅 admin。实测 `zhangwei` 调 `/v1/users` 403。只读外部审计场景用 `external_auditor`（readonly，仅 public）。
4. **业务问答**：基于真实语料回答（如「员工年假天数」→ 引用《员工年假管理制度》）。

## 2026-08-15 - 企业级改造 Phase 1（已交付）

Status: implemented

把「面试 demo 属性」的平台产品化为企业级项目：真实登录认证、用户/租户/文档注册表（PostgreSQL）、文档管理 API、前端产品 UI。核心内容：

1. **PostgreSQL 注册表**：`tenants`/`users`/`documents` 表 + `internal/migrations`（embedded SQL 自动迁移）；upload/worker 写穿文档状态，删除级联写透。跨租户删除 bug 修复（Qdrant/ES 删除按 tenant 过滤）。
2. **真实认证**：`POST /v1/auth/login`（bcrypt → JWT），`BOOTSTRAP_ADMIN_*` 首启建初始 admin；admin 经 `/v1/users`、`/v1/tenants` 管理用户/租户。
3. **文档管理 API**：`GET /v1/documents`（分页/权限矩阵过滤/搜索）、`GET|DELETE /v1/documents/{id}`（租户隔离 404）。
4. **前端产品化**：登录页 + HttpOnly cookie 鉴权 + 侧边栏产品 UI（问答/文档管理/用户管理/数据接入/可观测/检索质量/Agent）。
5. **demo 资产清理**：面试文档归档至 `docs/archive/`，demo 脚本归档至 `scripts/archive/`，`seed-demo-data.py` → `load-corpus.py`（经登录灌语料）。

## 2026-08-15 - 企业级改造 Phase 2（已完成部分）

Status: implemented

1. **审计日志**：`audit_logs` 表（migration 0002）+ `internal/audit` 包（追加式 Store + List）；login（成功/失败/禁用）、upload、document delete 埋点（best-effort，不阻塞主操作）；`GET /v1/audit` admin 查看 API（租户隔离 + action 过滤）；前端 `/audit` 审计日志页（admin）。
2. **token 撤销**：`users.token_version` 列（migration 0003）+ `SetPasswordHash` 递增；JWT 携带 `token_version`，`Verifier` middleware 对 DB 中的用户校验版本/active（`NewVerifierWithStore`），密码重置后旧 token 失效。离线/测试 token（非 UUID UserID 或 DB 无行）放行，JWT 签名保护。
3. **租户内 admin 隔离**：admin 仅管理本租户用户——创建用户强制使用调用者 JWT 的 tenant（忽略请求体）；update/delete/重置密码经 `requireTenantUser` 校验，跨租户目标一律 404（不透漏存在性）。

## 2026-08-15 - 企业级改造 Phase 2（剩余）

Status: planned

0. **负例拒答兜底（实验完成，结论：分数门控不可行 → 生成后忠实度验证）**：负例（无权访问的文档）检索正确不透漏，但 LLM 未输出拒答句导致 `negative_case_missing_not_found_fallback` 失败。
   **实验结论（2026-08-16，bge-m3）**：
   - 离线 pairwise：正负分布重叠宽度 **0.2271**（nomic 时 0.2784），绝对阈值无干净分离点；
   - 在线真实 eval（独立栈，bge-m3 + deepseek，44 语义集）：**26/38 正例命中时 max_relevance 恰好 0.5**（strict_rank=1，dense cosine 真实值），唯一失败的负例 sem-014 候选最高分也是 0.5 → 正负分数完全重合，`RETRIEVAL_MIN_RELEVANCE` 硬门控不可用（任何阈值都误杀正例，ADR 0006 重测结论维持）；
   - 现状：6 个负例中 **5 个已由 deepseek 遵循 prompt 拒答**，仅 sem-014 失败——其 query 是权限机制提问（非机密内容提问），检索返回 5 个弱相关公开候选（max_rel=0.5），LLM 误判有据而编答。
   **根治方向（2026 企业级实践：生成后忠实度验证为主导机制，阈值不是主导）**：
   1. query-api 生成后校验答案声明的可溯源性（LLM-based grounding check，或确定性词/数字级验证），验证不过 → 固定拒答句「未找到相关文档，无法回答该问题。」；sem-014 的编造声明无法从弱相关候选溯源，正例可从目标文档溯源；
   2. mock 负例用确定性候选相关性规则（mock hash embedding 有区分度，无高相关候选 → 拒答）；
   3. 验证决策进 `retrieval_info` + 报告，保持可观测；
   4. 回归验证：真实负例拒答 100% + 正例 Recall@1 不降（`--min-hit-rate 0.90` 把关检索）。
   验收：mock + real 的负例拒答率 100%，且正例 Recall@1 不降。
   **已实施（2026-08-16，real 达成，mock 待）**：
   - query-api 生成后 grounding check（`RETRIEVAL_GROUNDING_CHECK` 默认 true，模糊带 0.45–0.70）：答案生成后由校验器 LLM 判定答案关键断言（事实/数字/结论）是否可溯源到检索来源，判 false → 固定拒答句；决策进 `retrieval_info.grounding_checked/grounding_passed`。真实验证正例 28/28 通过、无误伤。
   - sem-014 根因确认：其 query 问权限机制，被 user **合法可见**的 internal 规则文档（sem-006「三个可见级别」）合理回答（答案逐字复述 sem-006），grounding 判 supported 正确 → **评测数据缺陷非系统缺陷**。已把 query 改为「项目机密资料涉及的项目成员名单是什么？」（指向机密实体，与可见文档 max 相似度 0.429，避开 sem-006）。
   - 验证（bge-m3 语义集）：6/6 负例拒答（含修正后 sem-014），正例 hit_rate 0.921 不降；剩余 3 失败（sem-002/013/033）为既有 `retrieval:timeout` 检索噪声。
   - mock 验证（2026-08-16）：mock 语义集负例 6/6 仍失败，且 **确定性候选规则对 mock 不可行**——mock 的 hash relevance 在 0.5 处饱和（正例 median 与负例 5/6 均恰为 0.5，无分离阈值），mock LLM 输出固定模板（「基于参考文档回答」）不遵循拒答。mock 负例拒答属 mock 语义能力边界（mock 仅验链路，CLAUDE.md 明示其指标非质量证据），不作为质量门禁；CI 门禁（golden-set 锚点集，--min-hit-rate/pass-rate/answer-pass-rate 0.90）不受影响。
1. **文档搜索/详情深化（已交付 2026-08-16）**：
   - `GET /v1/documents/search?q=`：ES BM25 全文检索聚合到文档级（tenant+permission 过滤，docstore 富化 file_name/status，bestScore 排序；ES 关闭 → 503）。
   - `GET /v1/documents/{docID}/chunks`：Qdrant scroll 按 doc+tenant+permission 返回全部切块（content/index/metadata，按 index 排序）；权限复用 `permissionAllowed`（跨租户/无权限 404）。store 层 `QdrantStorer.ListChunksByDoc`。
   - 前端：文档详情页 `/documents/[id]`（元数据卡片 + chunks 手风琴，复用问答页引用展开模式）；列表页内容检索框 + 详情链接；apiClient/types/代理路由。
   - 顺带修复：`parseUploadMetadata` 空 metadata 返回 nil → PG NOT NULL 违反（任何不带 metadata 的上传都会失败）→ 返回空 map。
   - 测试：store 3 + handler 8 单测、web build、e2e-smoke（search/chunks 断言）全通过。
   - e2e-smoke 加固：改为独立 compose project `ai-etl-smoke` + 独立端口（eval override 隐藏后端端口 + smoke override 仅暴露 query-api:8081）+ BOOTSTRAP admin 建在上传同租户。**原因：cleanup `down -v` 曾删掉共享主栈 volumes（数据丢失）**。
2. **前端硬化（已交付 2026-08-16）**：
   - **HttpOnly cookie 复核**：`ai_etl_token` 已确认 httpOnly + sameSite=lax + path=/ + secure(生产)；补齐 `maxAge=24h`（匹配 token TTL，避免静默过期）；logout 正确 `maxAge=0` 清除。
   - **retrieval 元数据**：问答页「检索链路」面板补齐 #0 新增的忠实度校验展示（`grounding_checked`/`grounding_passed` → 未触发 / 通过（可溯源）/ 拦截（无据拒答））；`RetrievalInfo` 类型补字段。
   - web build 通过。

## 2026-08-08 - AI Engineer Roadmap

Status: planned

Goal: 把项目从「带 RAG 外壳的分布式后端」改造成「能量化讲清 AI 质量的 AI 应用工程项目」。

完整计划、优先级、跟进表见 `docs/ai-engineer-roadmap.md`。摘要：

1. P0 建立可信评测基线：当前 100% 的评测结果基于 8 维哈希 mock embedding 与锚点式 golden set，不构成质量证据。需支持真实模型、重建语义数据集、产出对照实验、补 token/成本可观测。
2. P1 修确定性缺陷：中文 token 估算偏差 2-4 倍、topK 无上限、主查询链路缺 prompt injection 防护、默认模型过旧、仓库残留文件。
3. P2 补 AI 侧短板：SSE 流式 + TTFT、演示前端、prompt 版本管理、Agent 评测与 native tool calling。
4. P3 架构改进：Redis 单实例混装 durable 状态与 LRU 缓存（正确性问题，阶段 1 提到 P1）、审批审计 24h TTL 后消失、无文档删除与重建索引、双写无对账、query-api 单体承载查询与 Agent 编排。
5. P4 面试叙事：基于真实数据重写讲解材料，补选型 ADR。

## 2026-07-13 - Module 4 Engineering Learning Evaluation

Status: implemented and executed

Goal: provide a repeatable 100-case learning and regression evaluation without misrepresenting synthetic architecture cases as enterprise historical evidence.

Plan:

1. Maintain 100 deidentified synthetic cases across ETL, retrieval, Agent, and observability, including permission-refusal negatives.
2. Validate the set for structure, reference answers, and high-confidence sensitive patterns.
3. Run it in a unique Compose project with dynamic host ports so cleanup cannot affect the default development stack.
4. Require retrieval, answer, and final assertions for positive and negative cases.
5. Keep production acceptance separate: use the ignored, reviewer-approved private historical set before making user-quality or Judge claims.

## 2026-07-13 - Module 4 LLM Alerting and Dashboard

Status: implemented

Goal: detect LLM dependency degradation and deliver actionable enterprise alerts without inventing streaming metrics for a non-streaming API.

Plan:

1. Add low-cardinality LLM request outcome, latency, and process-local consecutive failure metrics.
2. Route LLM calls through the existing circuit breaker and classify bounded failure outcomes.
3. Add Prometheus alerts for five consecutive failures, rolling error rate, and p95 latency.
4. Add rule tests proving failure streak trigger and success reset behavior.
5. Add Alertmanager grouping, critical routing, inhibition, and authenticated webhook delivery.
6. Add a secret-backed WeCom/DingTalk adapter with DingTalk HMAC signing.
7. Provision a Grafana overview dashboard for Query, LLM, circuit, and Agent signals.
8. Add observability validation to required CI checks.
9. Preserve non-root runtime identities when local Compose secret bind mounts retain host ownership.
10. Validate private historical evaluation datasets for case count, reference answers, structural integrity, and high-confidence sensitive patterns before Judge execution.

## 2026-07-13 - Module 4 Optional LLM-as-a-Judge

Status: implemented

Goal: add a structured, optional answer-quality judge without weakening deterministic CI gates.

Plan:

1. Define strict structured scores for faithfulness, correctness, relevance, overall pass, reasons, and unsupported claims.
2. Add an OpenAI-compatible Judge client with bounded retries and response validation.
3. Run Judge scoring while Query answers and retrieved contexts are still in memory.
4. Add per-case Judge results, aggregate scores, thresholds, and errors to JSON/Markdown reports.
5. Keep Judge disabled by default and preserve deterministic retrieval/answer assertions as required CI checks.
6. Add a manual GitHub Actions workflow using `JUDGE_API_KEY` and report artifacts.
7. Test schema requests, retries, invalid scores, mock HTTP compatibility, and report generation.

## 2026-07-13 - Module 4 Query Trace Topology

Status: implemented

Goal: make a Query request traceable from the HTTP boundary through retrieval stages and final LLM generation.

Plan:

1. Continue incoming W3C `traceparent` headers and return `X-Trace-ID` to API callers.
2. Propagate trace context to embedding, Qdrant, Elasticsearch, reranker, and LLM HTTP requests.
3. Add Query, prompt construction, LLM generation, retrieval, cache, route, backend search, fusion, and rerank spans.
4. Expose `X-Trace-ID` to browser clients through CORS.
5. Add tests for HTTP trace propagation and retrieval-stage span topology.
6. Verify focused packages and the full Go suite before completion.

## 2026-07-05 - Module 3 Agent Observability and Alerts

Status: implemented

Goal: make Agent orchestration visible through low-cardinality metrics and production alert rules.

Plan:

1. Add Agent run creation metrics with `auto_execute` label.
2. Add terminal run completion and duration metrics with `state` and normalized `error_type` labels.
3. Add tool-step count and duration metrics with `tool_name` and `state` labels.
4. Add durable approval-decision metrics with `decision` and `tool_name` labels.
5. Wire Agent API lifecycle mutations into a small observer interface instead of coupling Agent API directly to Prometheus.
6. Emit events only when a run or step changes state, avoiding duplicate counts from reads or repeated requests.
7. Add Prometheus alerts for Agent failures, lifecycle timeouts, and high p95 run duration.
8. Cover metrics and Agent API observer behavior with tests.

## 2026-07-04 - Module 3 Agent Run Lifecycle Governance

Status: implemented

Goal: make Agent runs operationally controllable after creation, especially for hung executions and stale human approvals.

Plan:

1. Add explicit run cancellation with tenant isolation and owner-or-approver authorization.
2. Persist cancellation metadata: cancelled by, cancel reason, and cancelled at.
3. Mark the active step as cancelled when a non-terminal run is cancelled.
4. Add run-level timeout enforcement before planning or tool recovery.
5. Add approval timeout enforcement for runs stuck in `pending_approval`.
6. Reject pending approval audit records when a lifecycle timeout or cancellation closes the run.
7. Prevent terminal runs from being resumed or cancelled through the HTTP API.
8. Expose `POST /v1/agent/runs/{id}/cancel`.
9. Add `AGENT_RUN_TIMEOUT` and `AGENT_APPROVAL_TIMEOUT` configuration and Compose/env templates.
10. Cover cancel, timeout, authorization, tenant isolation, terminal conflict, and approval-audit synchronization with tests.

## 2026-07-04 - Module 3 Redis Distributed Lock

Status: implemented

Goal: make the Agent Orchestrator safe for multi-replica `query-api` deployments.

Plan:

1. Add a Redis-backed `LockManager` for Agent runs.
2. Store short-lived lock ownership separately from a longer-lived monotonic fencing token.
3. Reject any non-expired lock acquisition, including same-owner reacquire; use `Extend` for lease renewal.
4. Keep stale owner release from deleting a newer lease.
5. Preserve fencing token growth across release and reacquire so stale writes remain rejected by the store layer.
6. Use `MemoryLockManager` in dev/test and `RedisLockManager` outside dev.
7. Close Redis lock resources together with Agent run and approval stores.
8. Add lock tests for competing owners, same-owner reacquire rejection, lease extension, stale release, and optional real Redis contract verification.

## 2026-07-04 - Module 3 External Approval and Audit

Status: implemented

Goal: replace request-local approval with durable, tenant-scoped approval records for side-effecting Agent tools.

Plan:

1. Add `ApprovalRequest` and `ApprovalStore` with in-memory and Redis-backed implementations.
2. Use deterministic per-run-step approval ids so pending approval creation is idempotent.
3. Create an approval record whenever an Agent run reaches `pending_approval`.
4. Add `GET /v1/agent/runs/{id}/approvals`, `POST /v1/agent/runs/{id}/approve`, and `POST /v1/agent/runs/{id}/reject`.
5. Require `agent:approve` for approve/reject while keeping approval listing tenant-scoped.
6. Persist approval decisions with approver, decision time, and reason.
7. Resume approved runs from the durable approval record so execution can recover after request interruption.
8. Add rejection handling that marks the pending tool step and run as failed without executing the tool.
9. Cover store isolation, approve, reject, tenant isolation, and approved-record resume behavior with tests.

## 2026-07-04 - Module 3 Task Status Tool

Status: implemented

Goal: add the second real read-only Agent tool and give it a real task-status read model instead of a fake lookup.

Plan:

1. Add `model.TaskStatus` and `model.TaskStatusStore`.
2. Add `internal/taskstatus` with in-memory and Redis-backed stores.
3. Add `TASK_STATUS_STORE` and `TASK_STATUS_TTL` configuration and environment templates.
4. Save `queued` status from `/v1/upload` before publishing to Kafka.
5. Save `processing`, `completed`, and `failed` statuses from the ETL worker pipeline.
6. Register `etl_task_status` in the Agent Registry with schema validation and `agent` permission.
7. Keep task lookup tenant-scoped so cross-tenant task ids return `not_found`.
8. Add tests for the status store, upload status write, worker status transitions, Agent tool execution, tenant isolation, and LLM Planner selection of the new tool.

## 2026-07-04 - Module 3 LLM Planner Integration

Status: implemented

Goal: make the Agent Orchestrator use a real model-backed planner outside development while preserving deterministic dev/test execution.

Plan:

1. Add `LLMPlanner` backed by an OpenAI-compatible chat-completions endpoint.
2. Require planner output to be a structured JSON decision: `tool_call` or `final`.
3. Expose Registry tool definitions to the planner without exposing handlers.
4. Reject unregistered tools, invalid decision JSON, invalid tool arguments, and empty final answers before the orchestrator executes anything.
5. Add `AGENT_PLANNER_TYPE=auto|llm|rule`; resolve `auto` to `rule` in dev and `llm` outside dev.
6. Add LLM planner endpoint, API key, model, timeout, and max-token configuration.
7. Reject `AGENT_PLANNER_TYPE=rule` in production to keep RulePlanner as dev/test fallback only.
8. Add tests for LLM tool-call planning, final planning, guardrail failures, planner selection, and production config validation.

## 2026-07-04 - Module 3 CI and E2E Hardening

Status: implemented

Goal: unblock CI/deterministic eval and prove the Agent API works through real HTTP boundaries.

Plan:

1. Move Agent Planner validation from shared worker/API config validation into `ValidateAPI()`.
2. Keep production Query API guardrails for LLM Planner while allowing worker startup without Agent Planner credentials.
3. Expose Agent runtime/planner environment variables in `docker-compose.yml` for `query-api`.
4. Persist successful tool-call steps as `completed` for correct audit semantics.
5. Verify full Go tests, compose config, RAG smoke, `rag_query` Agent HTTP E2E, and `etl_task_status` Agent HTTP E2E.

## 2026-07-04 - Module 3 Agent API Integration

Status: implemented

Goal: expose the stateful Agent Orchestrator through a minimal HTTP API and connect it to one real platform capability.

Plan:

1. Export `query.Service.Ask` so tools can reuse the existing retrieval and generation pipeline in-process.
2. Add authenticated actor propagation for user id, role, and scopes.
3. Add `internal/agentapi` with `POST /v1/agent/runs`, `GET /v1/agent/runs/{id}`, `POST /v1/agent/runs/{id}/resume`, and `POST /v1/agent/runs/{id}/approve`.
4. Register `rag_query` as the first real read-only Agent tool backed by Query Service.
5. Add a deterministic first planner that calls `rag_query` once and finalizes from the tool result.
6. Add Agent configuration for node id, max steps, lock TTL, and run TTL.
7. Normalize Agent API metric labels to avoid high-cardinality run ids.
8. Add tests for RAG-backed run creation/execution, run lookup, approval, and config defaults/overrides.

## 2026-07-04 - Module 3 Agent Orchestrator MVP

Status: implemented as internal core package

Goal: introduce the first stateful Agent Orchestrator slice without coupling it to HTTP or a real LLM planner yet.

Plan:

1. Add `internal/agent` with durable `Run`, `Step`, state, planner, tool, store, lock, and policy types.
2. Add a strict Tool Registry that validates JSON arguments before invoking handlers.
3. Add RBAC-style tool authorization with required permissions and approval checks.
4. Add an Orchestrator that acquires a per-run lock, persists state transitions, executes tools, and enforces max-step failure.
5. Add in-memory Store and LockManager implementations for deterministic tests.
6. Add version and fencing-token checks to reject stale writes from expired owners or concurrent writers.
7. Add a Redis-backed Agent Store for process-independent run persistence.
8. Add waiting-tool recovery so persisted tool calls resume with the same idempotency key instead of asking the planner for a new action.
9. Add pending approval, side-effect registration guardrails, and compensation handlers for failed side-effecting tools.
10. Add tests for schema validation, authorization, idempotency key generation, state persistence, recovery, max-step guardrails, lock takeover, stale lock rejection, stale write rejection, approval, and compensation.
11. Document the MVP boundaries and next integration steps in `docs/agent-orchestrator-design.md`.

## 2026-06-28 - Enterprise Rerank Policy

Status: implemented, superseded by Full Enterprise Rerank Guardrail

Goal: make the optional Cross-Encoder reranker behave like an enterprise retrieval stage instead of reranking every query.

Plan:

1. Add `RETRIEVAL_RERANK_POLICY` with `auto` as the default and `always` for offline experiments.
2. Keep exact identifier and keyword queries on fused BM25/vector ranking in `auto` mode.
3. Allow semantic and hybrid queries to call the reranker when the service is enabled and configured.
4. Preserve fallback behavior: if the reranker fails, return fused ranking with a partial error.
5. Cover the policy with unit tests and update operator-facing documentation.

## 2026-06-28 - Rerank Policy Benefit/Risk Eval

Status: implemented

Goal: prove the `auto` policy is not only avoiding regressions, but is routing by the cases where reranking helps or hurts.

Plan:

1. Add a deterministic retrieval-engine test where semantic fused ranking is intentionally worse than reranked order.
2. Add a deterministic exact-query test where fused BM25/vector ranking is better and forced reranking would regress.
3. Document the focused eval command under `docs/evals/README.md`.

## 2026-06-29 - Candidate-Aware Exact Evidence Protection

Status: implemented, superseded by Full Enterprise Rerank Guardrail

Goal: reduce dependence on finite keyword/regex routing for exact queries by checking whether retrieved candidates contain strong exact tokens from the query.

Plan:

1. Extract strong exact tokens from the query, including email, UUID, phone-like numbers, structured IDs, and mixed alphanumeric tokens with separators.
2. Check fused candidates for exact token evidence in `doc_id`, `chunk_id`, or `content`, with separator normalization.
3. Originally skipped reranker when exact candidate evidence existed; this was superseded by protective rerank + pinning in the full guardrail.
4. Add tests for normalized exact evidence and an unrouted exact-token query that old query-only routing would classify as semantic.

## 2026-06-29 - Full Enterprise Rerank Guardrail

Status: implemented, completed with no-reranker exact pinning

Goal: complete the enterprise rerank design across rule, business schema, retrieval signal, protective rerank, and observability/eval layers.

Plan:

1. Add document/task/chunk `metadata` propagation from upload to parser, Qdrant payload, Elasticsearch document, and retrieval candidates.
2. Add `RETRIEVAL_EXACT_SCHEMA_FIELDS` so configured business fields such as `contract_no`, `trace_id`, and `customer_ref` participate in exact evidence.
3. Add Elasticsearch metadata exact-term `should` clauses so metadata-only identifiers can be recalled even when the content body does not contain the token.
4. Replace candidate-evidence skip with protective rerank in `auto`: semantic/hybrid queries may use reranker, then exact-match candidates are pinned above non-exact candidates.
5. Add structured rerank-decision logs with route, policy, rerank decision, exact token hashes, matched candidate count, top-rank changes, and protection state.
6. Extend deterministic unit tests and golden eval data with metadata-only exact retrieval coverage.
7. Pin exact-evidence candidates even when reranker is disabled or unconfigured, so CI/default deployments keep the same deterministic business-identifier protection.

## 2026-06-29 - No-Reranker Exact Candidate Pinning

Status: implemented

Goal: close the CI/default deployment gap where metadata-only exact candidates could be recalled but still lose fused ranking when no reranker service was configured.

Plan:

1. Reuse the same exact-evidence planner when `RETRIEVAL_ENABLE_RERANK=false` or `RERANK_ENDPOINT` is empty.
2. If semantic/hybrid candidates contain exact evidence, apply `protectExactMatches` directly to fused candidates.
3. Log the decision with reason `reranker_not_configured_exact_candidate_pinned` for observability.
4. Add an engine-level regression test where `customer_ref` metadata is the only exact evidence and the fused top candidate is a distractor.
5. Rerun Go tests and CI-like deterministic eval with reranker disabled.

## 2026-06-29 - Module 2 Lightweight Load Test

Status: implemented

Goal: produce a lightweight performance baseline for the completed Hybrid Retrieval & Reranking Engine.

Plan:

1. Extend the query load-test script to report scenario name, throughput, top-hit rate, and business metadata exact-match cases.
2. Run cache-miss rerank-off, cache-miss rerank-on, cache-hit rerank-on, and schema exact rerank-on scenarios with low concurrency.
3. Use the benchmark to verify latency impact from CPU Cross-Encoder reranking and latency reduction from Redis semantic cache.
4. Fix the exact-route guardrail gap found during schema exact pressure testing.
5. Document the benchmark result in `docs/module2-load-test-report.md`.

## 2026-08-19 - Legacy Word DOC Parsing

Status: implemented

Goal: parse genuine binary Microsoft Word `.doc` uploads instead of routing them to the DOCX-only `python-docx` parser.

Plan:

1. Add a regression test at the `parse_document(path)` boundary for a legacy `.doc` conversion.
2. Convert `.doc` files with a dedicated, headless LibreOffice parser using a per-request profile and bounded execution time.
3. Keep `.docx` parsing on `python-docx` and preserve the original upload size in parser responses.
4. Install the required LibreOffice Writer runtime in the parser-service image.
5. Rebuild the service and replay the original failed MinIO object through the parser API.
6. Run parser tests and the repository verification suites, then update operator documentation and the learning log.

## 2026-08-19 - Document Content Search Web Proxy

Status: implemented

Goal: make document content searches from the Web document manager preserve the `q` and `limit` query parameters required by Query API.

Plan:

1. Add a Web-boundary smoke assertion for `/api/documents/search?q=...` using the session cookie.
2. Add a dedicated static Next.js route that forwards the complete query string and authentication token.
3. Build and redeploy the Web service, then verify the reported Chinese query through the live proxy.
4. Run TypeScript, Compose, unit, and isolated end-to-end verification.
5. Update the learning log and commit the focused fix.

## 2026-08-25 - Completed User Upload Publication and Search Recovery

Status: implemented

Goal: make a successfully ingested document in the default user upload space
immediately searchable, and prevent the local single-node Elasticsearch service
from becoming read-only while usable disk headroom remains.

Plan:

1. Publish documents in `user-uploads` only when their ETL status becomes
   `completed`; preserve manual publication state for every other knowledge
   space and for processing or failed uploads.
2. Add a regression assertion at the document-store update boundary.
3. Configure absolute Elasticsearch disk watermarks suitable for this local
   single-node stack and document their environment overrides.
4. Clear the existing read-only block, replay only the affected indexing retry
   records, and verify both indexes contain the complete chunk set.
5. Rebuild and redeploy only the ETL worker, then verify a real governed query,
   full Go tests with race coverage, `go vet`, Compose configuration, stack
   health, and diff integrity.

## 2026-08-25 - Public HTTPS Domain

Status: implemented

Goal: expose the existing Web application at `https://rag.ipuau.com` without
changing application routes or disrupting the existing host Nginx sites.

Plan:

1. Confirm the authoritative DNS provider and point the `rag` record at the
   current server.
2. Add a versioned Nginx virtual host that proxies to the existing Web service,
   preserves forwarding headers and long-lived responses, and enforces the
   upload boundary before forwarding.
3. Issue and deploy a Let's Encrypt certificate, redirect HTTP to HTTPS, and
   enable the Web session cookie's Secure flag.
4. Verify public and origin HTTPS, login and protected-route boundaries, the
   upload size limit, certificate renewal, the existing blog, and stack health.

## 2026-08-25 - P2.1 Document Publication Governance Agent

Status: implemented

Goal: replace the duplicate query Agent page with one bounded enterprise
workflow that assesses a managed-space draft, requests human approval, publishes
through shared governance rules, and verifies the outcome.

Plan:

1. Add a deep publication-workflow module whose interface owns deterministic
   readiness assessment, durable review records, approved publication, cache
   invalidation, and audit behavior.
2. Keep `user-uploads` auto-publication unchanged; governance runs apply only to
   managed knowledge spaces and fail closed on incomplete ETL, inactive state,
   missing indexes, or unresolved version metadata.
3. Register bounded Agent tools for document inspection, readiness assessment,
   and approval-required idempotent publication; never let planner output bypass
   module policy.
4. Add PostgreSQL-backed publication review records and enforce tenant scope,
   administrator approval, and requester/approver separation.
5. Replace the free-form Agent UI with a draft-document governance workbench,
   run timeline, blockers, approval actions, cancellation, and durable results.
6. Test the workflow at the publication module interface, Agent HTTP interface,
   and Web interface, including negative authorization, replay, rejection,
   recovery, and unchanged `user-uploads` behavior.

Outcome:

- Added deterministic `assess_document_publication` and approval-required
  `publish_document` tools. Free-form planner output cannot select or reorder
  the managed publication path.
- Readiness now fails closed on document lifecycle/metadata and exact Qdrant +
  Elasticsearch tenant/document chunk counts. `user-uploads` auto-publication
  remains unchanged.
- Approval records are stored in PostgreSQL and requester self-approval is
  rejected. Publication is idempotent, flushes semantic cache, and appends the
  existing immutable document audit event.
- Replaced the duplicate query Agent page with a managed-draft workbench showing
  checks, blockers, action history, human approval, rejection, and cancellation
  without exposing planner thoughts.
- Full Go tests, Race/coverage, vet, Web lint/typecheck/build, Compose config,
  and diff checks pass. Live deployment verified both blocker and ready paths;
  self-approval returned 403 and the test run was cancelled without publication.

## 2026-08-25 - P2.1 Acceptance and Deterministic Eval Compatibility

Status: implemented

Goal: complete a real two-administrator publication acceptance and keep the
deterministic CI evaluator compatible with completed `user-uploads`
auto-publication.

Plan:

1. Run one synthetic managed draft through requester assessment, self-approval
   rejection, independent administrator approval, publication, retrieval, and
   PostgreSQL approval/audit verification.
2. Treat the default evaluation corpus as `user-uploads`: after completed ETL,
   verify its publication state instead of issuing a duplicate direct PATCH.
3. Add regression tests for the published and unpublished verification paths,
   then rerun the exact deterministic CI command.

Outcome:

- The live workflow completed with distinct requester and approver identities;
  the document became searchable and durable approval/audit rows matched the
  second administrator.
- Replaced the evaluator's obsolete publication PATCH with a read-only state
  assertion. The full 47-case mock evaluation passed all release thresholds.
