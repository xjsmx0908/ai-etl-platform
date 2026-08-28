# ADR 0008: Track generation-scoped manifests for derived indexes

## Status

Accepted (2026-08-28); pipeline and active-generation query integration implemented

## Context

Qdrant and Elasticsearch are derived projections of PostgreSQL catalog data.
The pipeline currently treats Qdrant as primary, tolerates partial batch
success, and sends Elasticsearch failures to a Redis retry queue. A permanent
retry failure can leave the two indexes divergent. Counting chunks in each
backend before managed publication is useful, but equal counts do not prove
that both contain the same deterministic chunk identities, and the expected
parser output is not stored as a durable manifest.

Model, chunking, or schema changes also require rebuilding indexes without
making a partially built projection visible to queries.

## Decision

Add a PostgreSQL index manifest for each tenant, document version, and generation.
A manifest records:

- stable generation ID and document version ID;
- chunker, embedding model, vector dimension, schema, collection, and index
  versions;
- expected chunk count and digest of sorted deterministic chunk IDs/content
  hashes;
- observed Qdrant and Elasticsearch counts/digests;
- `building`, `ready`, `failed`, `active`, or `retired` state; and
- attempt, error, and verification timestamps.

An indexing module will own building and verifying a complete generation behind
one interface. Backend-specific Qdrant and Elasticsearch adapters remain
internal seams. A generation becomes `ready` only when both projections match
the expected identity set. Publication and query resolution may use only the
active generation for the current document version.

Activation is a PostgreSQL compare-and-set operation. The previous active
generation remains queryable until the new generation is ready and activated.
Retired generations are removed asynchronously after a configurable rollback
window. A reconciler periodically compares manifests with both backends and can
repair missing projections idempotently.

## Invariants

- `completed` ETL means a verified ready generation, not merely one successful
  backend write.
- At most one generation is active for a document version.
- Query and publication paths never use `building`, `failed`, or `retired`
  generations.
- Equal counts without matching identities are not sufficient for readiness.
- Reindexing does not require taking the prior active generation offline.

## Consequences

- Index divergence becomes durable, measurable, and repairable.
- Reindex, model migration, rollback, and cleanup share one lifecycle model.
- Storage temporarily increases during generation replacement.
- Deterministic chunk identity and backend generation filters become required.
- The current Redis retry queue can remain a short-term delivery mechanism, but
  the PostgreSQL manifest, not the queue, owns correctness and repair status.

## Alternatives considered

**Use backend counts only.** Rejected because equal counts can hide different or
stale chunk identities.

**Fail the whole document on any Elasticsearch error.** Rejected as the only
mechanism because it couples ingestion availability to one projection without
providing safe rebuild and rollback.

**Make Qdrant authoritative.** Rejected because vectors are a rebuildable
projection and do not contain the complete catalog, policy, or generation state.

## Acceptance gate

Tests must cover partial Qdrant writes, Elasticsearch outage and dead-letter,
identity mismatch with equal counts, process restart, concurrent activation,
safe rollback, reindex, and garbage collection. Metrics and alerts must expose
manifest age, failed generations, backend divergence, and repair outcomes.

## Implementation progress

The first vertical slice adds migration `0011_index_manifests`, a deterministic
generation-scoped chunk identity digest, and a narrow PostgreSQL manifest
interface. A document version is the durable ingestion `job_id`, protected by a
composite foreign key with tenant and document identity. Identical manifest
creation is idempotent; a conflicting immutable definition fails closed.
PostgreSQL enforces at most one active generation per tenant and document
version. Readiness and activation use compare-and-set predicates; activation
compares the expected current generation while holding a transaction-scoped
identity lock, so stale writers fail and a failed activation rolls back
retirement of the previous generation. Failed builds have a controlled retry
transition that clears stale backend observations before rebuilding.

The second vertical slice adds generation-aware projection adapters. Qdrant
points and Elasticsearch documents carry tenant, document version, generation,
and content-hash fields; physical identities include the generation so a rebuild
cannot overwrite the prior generation. Both adapters enumerate exactly one
generation and produce the same deterministic digest for verification. Existing
non-generation writes remain compatible until the pipeline cutover.

The third vertical slice connects durable outbox ingestion to a generation
builder. It persists an unsealed `building` manifest before the first backend
write, streams idempotent generation-scoped writes to both backends, seals the
expected count/digest after parsing, verifies both observations, and activates
with expected-current compare-and-set. Generation IDs are deterministic over
the durable job identity and complete build configuration, so redelivery repairs
the same generation. Any embedding, Qdrant, Elasticsearch, verification, or
activation failure prevents durable job completion and records a failed build.
Legacy non-outbox messages retain their existing compatibility path.
The manifest also persists the active predecessor observed when the build first
starts; delayed retries cannot adopt a newer winner and overwrite it.

The fourth vertical slice gates query evidence against PostgreSQL manifests.
Qdrant and Elasticsearch candidates carry document-version and generation
identity through retrieval, fusion, reranking, and semantic caching. One batch
manifest lookup admits only the matching `active` generation and rejects
`building`, `ready`, `failed`, `retired`, stale, and malformed identities.
Cached evidence is revalidated on every hit so activation cannot leave an old
generation queryable until cache expiry. Documents that have never had a
manifest retain legacy read compatibility; once a document is managed, legacy
points fail closed. PostgreSQL lookup failures also fail the query closed.

Reconciliation/repair, rollback and retention cleanup, bounded metrics/alerts,
and the remaining acceptance matrix are subsequent P2.3 slices.
