# ADR 0010: Gate production on approved SLO and recovery evidence

## Status

Proposed (2026-08-27)

## Context

The repository has health checks, metrics, alerts, load tests, and persistent
Compose volumes, but it does not define approved production SLOs, RPO/RTO,
retention, backup topology, or a recurring restore exercise. Without those
objectives, availability and disaster recovery cannot be accepted or tested.

Exact objectives depend on business criticality, traffic, deployment topology,
compliance, and budget. Engineering must not invent them silently.

## Decision

Production promotion requires an approved service profile that names an owner
and defines, at minimum:

| Objective | Required definition |
| --- | --- |
| Query availability | SLI, target, window, exclusions |
| Query latency | First-token and completion percentiles |
| Ingestion freshness | Accepted-to-ready percentile and maximum age |
| Quality | Gold dataset, retrieval, answer, citation, and refusal gates |
| Recovery | RPO and RTO by PostgreSQL, objects, indexes, and audit data |
| Retention | Raw file, version, audit, trace, evaluation, and backup periods |
| Capacity | Tenant, document, chunk, ingestion, query, and model limits |

PostgreSQL catalog and audit data plus immutable source objects are backed up as
authoritative state. Qdrant and Elasticsearch remain rebuildable projections,
but the recovery plan must prove that rebuilding them meets the approved RTO.
If it does not, snapshots are also required.

Every production release must pass automated quality, security, and performance
gates. Restore and failover evidence has an explicit maximum age; stale evidence
blocks promotion. Error-budget exhaustion pauses non-remediation releases.

## Consequences

- Production readiness becomes evidence-based rather than inferred from healthy
  containers.
- Business owners must decide service criticality and budget before engineering
  selects topology.
- Backup, restore, capacity, and quality reports become retained release
  artifacts.
- Local Compose remains a development profile and is not described as highly
  available production infrastructure.

## Open decisions

- Named business and technical owners.
- Numeric SLO, RPO, RTO, capacity, and retention targets.
- Deployment regions/failure domains and data-residency constraints.
- Managed versus self-operated PostgreSQL, object, search, and model services.
- Maximum permitted age of restore and failover evidence.

## Acceptance gate

This ADR cannot become Accepted until its service profile is completed and
approved. Production promotion additionally requires a measured load test,
backup restore, index rebuild or snapshot restore, dependency-failure exercise,
alert delivery test, and rollback rehearsal against that profile.
