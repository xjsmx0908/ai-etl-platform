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
exact candidates, rejection, idempotency/conflict, and tenant isolation. R1 is
complete only after real-model scenarios and review-specific recovery/budget
termination have also passed.
