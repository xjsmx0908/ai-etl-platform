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
