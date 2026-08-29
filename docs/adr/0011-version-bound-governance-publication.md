# ADR 0011: Bind governed publication to an exact document version

## Status

Accepted (2026-08-29); P2.4-A through P2.4-D implemented

## Context

P2.1 introduced managed-document assessment, four-eyes approval, publication,
and audit. P2.2 made each admitted ingestion job a durable document version,
and P2.3 made generation manifests authoritative for projection completeness.
These capabilities are not yet one consistency model.

The publication workflow currently assesses document-level backend counts and
its approval names only `document_id`. A replacement can therefore arrive
between assessment and approval. The approval would then authorize a different
version from the one reviewed. Generation activation is also scoped to a
document version, so an old and replacement version can both be active even
though only one should be published. Direct deletion is synchronous and has no
durable recovery record when a dependency fails partway through cleanup.

## Decision

Introduce a PostgreSQL-owned publication record for each governed document.
It distinguishes the latest admitted version from the currently published
release:

- `current_version_id` identifies the replacement under review;
- `published_version_id` and `published_generation_id` identify the only
  governed release visible to queries; and
- a monotonically increasing revision supports compare-and-set publication.

Readiness is a deep publication-workflow module interface. It returns a sealed
publication candidate containing tenant, document, version, generation,
expected chunk count/digest, and revision. The active manifest must be healthy,
sealed, and exactly match both projection observations. Backend counts alone
are not publication evidence.

The approval tool arguments and durable approval row bind that candidate.
Publication revalidates it in PostgreSQL and atomically advances the published
release, appends the publication audit event, and rejects a stale candidate.
Cache invalidation remains an idempotent post-commit operation. A managed
replacement does not alter the published release until a new independent
approval succeeds; the prior release remains queryable meanwhile.

Managed query visibility requires an exact match with the published release in
addition to the active-generation health gate. `user-uploads` keeps its
documented automatic-publication policy, but records the same explicit release
identity after successful generation activation.

Deletion becomes a durable workflow: an accepted request first marks the
document non-queryable and records a deletion job and audit event. A leased,
idempotent collector records per-dependency progress for all generations and
source objects. Authoritative rows are finalized only after cleanup completes;
partial failures remain retryable and observable.

## Invariants

- Approval authorizes one immutable candidate, never a mutable document name.
- At most one release per managed document is query-visible.
- A replacement cannot publish itself or hide the prior approved release.
- Publication state and its success audit commit atomically.
- Accepted deletion immediately fails closed and survives process restarts.
- Tenant, permission, and independent-approver checks apply at every mutation.

## Consequences

The document catalog gains explicit current/published version identity and
publication revision. Publication, query visibility, replacement, and deletion
share PostgreSQL as their correctness seam. More historical objects and
generations remain temporarily, but retention stays disabled until its window
is approved. Existing legacy rows require a deterministic backfill and remain
fail-closed if their release identity is ambiguous.

## Alternatives considered

**Keep document-level count checks.** Rejected because equal counts do not prove
identity and cannot bind human approval to the reviewed bytes.

**Retire the old release when replacement upload begins.** Rejected because a
failed replacement would create an avoidable availability gap.

**Delete synchronously without a durable job.** Rejected because partial
dependency failures cannot be reconstructed reliably after request loss.

## Acceptance gate

Automated evidence must cover managed upload, exact-candidate assessment,
self-approval rejection, independent approval, publication, query, replacement
with old-release continuity, stale-approval rejection, approved cutover,
durable deletion, immutable audit correlation, tenant isolation, restart, and
Kafka/Qdrant/Elasticsearch/MinIO failure recovery. No production deployment or
retention enablement is part of this ADR.

## Implementation progress

P2.4-A adds one `document_releases` row per tenant/document with separate
current and published version identity, the published generation, a monotonic
revision, and explicit resolution status. Durable upload admission records the
current version in the same PostgreSQL transaction as the document, ingestion
job, and outbox event. Replaying the same version is idempotent; a replacement
increments the revision while retaining the prior published release.

The PostgreSQL release module exposes tenant-scoped lookup, current-version
recording, and compare-and-set publication. A candidate must match both the
current version and expected revision, so an intervening replacement or
publication makes it stale. The migration backfills only exact object/job and
active-generation matches. Multiple candidates abort migration; old rows with
no provable durable identity are retained as `unresolved` for fail-closed later
integration.

P2.4-B replaces document-level count assessment with a PostgreSQL candidate
for the current version's sealed, healthy active manifest. The Agent's durable
approval arguments bind the exact version, generation, expected chunk count and
digest, and release revision; tenant identity still comes only from the
authenticated actor. At approval, one transaction locks and revalidates that
manifest, advances the release compare-and-set, changes document publication
state, stores an idempotency-key/request-hash binding, and appends the success
audit. Failure or staleness rolls back every write.

P2.4-C enforces the published release at both the RAG retrieval and
Elasticsearch document-search seams. Visibility fails closed on missing,
unresolved, stale, incomplete, or unhealthy release identity. Replacement
uploads preserve permission and knowledge-space identity and leave the prior
release queryable until approved cutover. Completed `user-uploads` versions
advance the same release record through an idempotent automatic transition.

P2.4-D replaces synchronous best-effort deletion with a PostgreSQL acceptance
transaction and leased collector. Acceptance returns a durable job only after
publication authority is revoked, pending ingestion is cancelled, and an audit
correlation commits. The worker records independent Qdrant, Elasticsearch, and
exact-object-key progress; partial failure retries only unfinished work. Active
ingestion leases defer cleanup and stale claim tokens cannot finalize. The
catalog and release graph are removed only after every dependency succeeds.
