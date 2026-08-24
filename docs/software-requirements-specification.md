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

## Non-Functional Requirements

- Authorization and publication checks fail closed when the catalog is
  unavailable or incomplete.
- Existing document metadata remains readable during migration.
- Tenant and space filters are applied to both Qdrant and Elasticsearch.
- Policy behavior is covered at the catalog interface and HTTP seams; an E2E
  regression proves that same-topic documents in different spaces never mix.

## P0 Acceptance Criteria

An unauthorized space request returns 403. A draft or retired document is never
cited. An omitted space resolves deterministically to the user's default
production space. A catalog failure returns 503 and does not call the LLM.
