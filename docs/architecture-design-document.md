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
4. Fail-closed batch validation retains only published, active documents.
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
