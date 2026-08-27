# ADR 0007: Admit ingestion through a PostgreSQL transactional outbox

## Status

Accepted (2026-08-27)

## Context

The upload path currently writes an object, stores queued state in Redis,
publishes a Kafka task, and then writes the authoritative PostgreSQL document
row. A Kafka success followed by a registry failure is deliberately non-fatal.
Reconciliation can later recover a minimal row from Qdrant, but it cannot
reconstruct ownership, filename, governance metadata, or the original admission
decision. A Kafka failure can also leave an unreferenced object.

Kafka transactions do not close this gap because PostgreSQL and object storage
do not participate in the Kafka transaction.

## Decision

Introduce an ingestion-admission module whose small interface accepts one
validated submission and returns a durable receipt. Its PostgreSQL adapter will
atomically:

1. lock the tenant/document identity for replacement;
2. create the document version and ingestion job in `queued` state;
3. append an immutable outbox event containing a stable event and job ID; and
4. commit the catalog state before any Kafka publication is acknowledged.

The object is uploaded to an immutable versioned key before this transaction.
If admission fails, the object is marked for bounded garbage collection. The
HTTP request returns `202` after the PostgreSQL commit; Kafka availability is no
longer on the synchronous success path.

A relay claims pending outbox rows with `FOR UPDATE SKIP LOCKED`, publishes them
to Kafka, and records publication metadata. Delivery is at least once.
Consumers deduplicate by event/job ID and apply lifecycle transitions with
compare-and-set semantics. A new version never retires the currently published
version until its index generation is ready and its publication policy passes.

## Invariants

- Every accepted upload has an authoritative catalog row and durable outbox row.
- No Kafka task is authoritative without its PostgreSQL ingestion job.
- Duplicate delivery cannot create a second document version or lifecycle
  transition.
- A failed replacement leaves the prior published version queryable.
- Orphan object cleanup operates only on keys not referenced by a live document
  version or unexpired upload reservation.

## Consequences

- Kafka outages increase outbox age but do not lose accepted uploads.
- PostgreSQL becomes required for production ingestion admission; the worker may
  not silently treat catalog writes as optional for governed documents.
- Additional tables, a relay loop, retention policy, metrics, and repair tooling
  are required.
- Object storage and PostgreSQL still cannot commit atomically. Versioned keys,
  explicit reservation state, and garbage collection make that gap observable
  and recoverable.

## Alternatives considered

**Keep the current ordered writes and reconcile from Qdrant.** Rejected because
the vector payload cannot recover the complete admission and governance record.

**Use only Kafka transactions.** Rejected because they cannot atomically commit
PostgreSQL catalog state or object storage.

**Publish Kafka first and require the worker to create the registry row.**
Rejected because authorization and governance facts belong to the synchronous
admission decision, not a later consumer.

## Acceptance gate

Implementation requires tests for crash points before and after every durable
write, duplicate relay delivery, Kafka outage recovery, replacement failure,
and orphan cleanup. An isolated end-to-end test must prove that every `202`
response eventually reaches a terminal catalog state without losing the prior
published version.

## Implementation progress

The first P2.2 slice adds atomic document/job/outbox admission, immutable
versioned object keys, stable retry identities, leased relay claims, and Kafka
outage recovery. The existing upload response remains compatible and now also
returns `job_id` and `event_id`. Consumer-side terminal job transitions,
scheduled orphan collection, relay lag metrics, and the isolated end-to-end
acceptance gate remain follow-up P2.2 work. Replacement admission failure keeps
the old object and projections, but failure during chunk overwrite is not yet an
atomic replacement; ADR 0008 generation activation is required before claiming
that gate. This slice does not claim those remaining gates.

The second P2.2 slice adds consumer-side processing leases and terminal-state
deduplication. A worker atomically commits the ingestion job and document
terminal state before acknowledging Kafka. Repeated terminal events are ACKed
without reprocessing; active duplicates are retried, and expired leases are
recoverable. Partition offsets advance only across the completed prefix of
messages actually fetched, preventing concurrent workers from committing past
an earlier NACK. Scheduled orphan collection, outbox/job metrics and alerts, and
the isolated crash-point E2E gate remain open.
