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
the principal or browser token. OIDC/SCIM, provider group mapping, service
identity, production local-login disablement, MFA/session policy, and
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
