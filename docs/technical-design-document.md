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
