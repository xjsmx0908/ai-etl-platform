# Product Requirements Document

## Product Goal

Turn the current AI ETL/RAG platform into a governed enterprise knowledge
product where users understand which knowledge space supplied an answer and
administrators control what is eligible for use.

## Primary Users

- Readers ask questions inside an authorized knowledge space.
- Contributors upload documents into spaces where they have write access.
- Space managers create spaces and manage membership.
- Tenant administrators retain tenant-wide recovery and governance access.

## P0 Experience

The workbench displays a knowledge-space selector and sends its stable ID with
every question. The upload workflow requires a destination space. Document
management displays the space and publication status. When no space is chosen,
the backend uses the caller's default production space rather than inferring a
scope from search results.

New uploads begin as `draft`; existing completed, active production documents
are backfilled as `published` to preserve service continuity. Demo documents are
quarantined in a non-default demo space and cannot enter production answers.

## Success Measures

- Zero cross-space citations in deterministic isolation tests.
- Zero draft, archived, or superseded citations.
- All successful answers expose the resolved space and traceable citations.
- Existing user-upload documents remain queryable after migration.

## Later Phases

P1 adds review/approval workflow, richer authority and applicability metadata,
and transactional publication audit. P2 adds enterprise identity integration,
retention, disaster recovery, supply-chain controls, and SLO-driven deployment.
