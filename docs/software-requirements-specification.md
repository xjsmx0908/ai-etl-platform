# Software Requirements Specification

## Purpose

The platform ingests enterprise documents asynchronously and answers questions
only from authorized, published evidence. Tenant, knowledge-space, document
classification, and publication controls are enforced before generation.

## Functional Requirements

- A tenant may own multiple knowledge spaces and exactly one default production
  space.
- Users may list only spaces they can access. Tenant admins may access every
  space in their tenant.
- Uploads must target an authorized space. Contributors may upload; readers may
  only query.
- Completed ingestion does not imply publication. Only `published` documents
  may be used as RAG evidence.
- Queries resolve an explicit space or the caller's default space before
  retrieval. Search ranking must never select a space.
- Demo spaces must never participate in a production user's default query.
- Every exclusion must be observable in retrieval diagnostics without exposing
  unauthorized document content.
- Managed documents must pass a version-bound autonomous Agent pre-review
  before approval. The Agent may choose only registered read-only review tools
  and must return findings bound to the current exact candidate.
- Review Agent model configuration must reuse the RAG Query LLM endpoint,
  credentials, and model; operators must not maintain a second required set of
  semantic-review credentials.
- Model, tool, evidence, candidate-consistency, timeout, and budget failures
  must fail closed into a manual-review path. The Agent cannot approve or
  publish.
- Approval and review-state changes must emit durable, content-safe
  notifications. Delivery may retry or skip, but must not block or alter
  review, approval, or publication. Configured WeCom/DingTalk channels must
  receive a content-safe human message with a release-center path.
- External workflow engines may receive the same events and submit decisions
  through a token-authenticated callback that resolves an active tenant
  administrator and reuses the existing approval path. The callback must not
  bypass group membership, self-approval, or exact-candidate checks.
- Review reports expire after a configured TTL. Expired pending reports cannot
  authorize publication; the same exact candidate is automatically rereviewed.
  Published or rejected evidence is retained unchanged.
- Completed side-effecting Agent tools that register compensation must be
  reversed after a later run failure, cancellation, or timeout.

## Non-Functional Requirements

- Authorization and publication checks fail closed when the catalog is
  unavailable or incomplete.
- Existing document metadata remains readable during migration.
- Tenant and space filters are applied to both Qdrant and Elasticsearch.
- Policy behavior is covered at the catalog interface and HTTP seams; an E2E
  regression proves that same-topic documents in different spaces never mix.
- Persisted Agent Run/Step observations and the release review report must make
  the chosen tools, evidence, model, prompt version, and final recommendation
  auditable without persisting credentials.

## P0 Acceptance Criteria

An unauthorized space request returns 403. A draft or retired document is never
cited. An omitted space resolves deterministically to the user's default
production space. A catalog failure returns 503 and does not call the LLM.

## Agent Pre-review Acceptance

The deterministic release-center matrix must cover ordinary and confidential
approval, Agent failure, sensitive-data and prompt-injection findings, stale
exact candidates, rejection, idempotency/conflict, and tenant isolation. R1 also
requires the real-model four-scenario acceptance; that gate has now passed.

## Product Experience Acceptance

Web workbench walkthroughs must follow
[`product-experience-acceptance.md`](product-experience-acceptance.md).
Findings are recorded in [`../issues/findings-register.md`](../issues/findings-register.md).
This page-level gate does not replace the Agent pre-review matrix.
