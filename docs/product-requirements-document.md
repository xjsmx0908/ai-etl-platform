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

## Knowledge Release Center (approved design, 2026-09-03)

The publication entry point is named **Knowledge Release Center**, not Agent
Publication Governance. It presents the business release state of managed
documents; Agent execution details and Run IDs are secondary audit information.

Managed documents follow this flow:

```text
ETL complete -> deterministic eligibility gate -> Agent pre-review
             -> approval task -> administrator decision -> exact-version release
```

The eligibility gate is authoritative for ingestion completion, governance
fields, and the sealed Qdrant/Elasticsearch generation. The Agent produces a
review recommendation and evidence, but cannot edit metadata, alter access,
choose the authoritative conflicting document, approve itself, or publish.

In the first release, “Agent pre-review” is a bounded release-eligibility
review. It receives the deterministic assessment result and the exact active
candidate's chunks, records an untrusted recommendation/evidence envelope, and
runs bounded deterministic checks for exposed secrets, obvious personal data,
and prompt-injection phrases. These checks can escalate risk or block a
recommendation, but they are not a complete content-semantic, privacy, or
confidentiality classifier. Any future model-based content review must define
its own document-content input, structured output schema, evidence references,
prompt-injection handling, and fail-closed policy before being presented as a
security or compliance control.

Approval policy is deterministic and risk-based:

- `user-uploads` keeps automatic publication and does not enter this workflow.
- Ordinary managed documents require one administrator decision after a
  successful pre-review.
- `confidential` documents and deterministic high-risk cases require two
  different administrators.
- Agent failure creates an audited manual-exception task; it never silently
  becomes a successful review.

### P2.4-R2 approval groups and policies

P2.4-R2 adds tenant-scoped approval groups and server-side policy resolution.
Administrators can create or deactivate groups, add or deactivate members, and
define policies matching a knowledge space, document permission, minimum risk,
priority, required decisions, and requester self-approval. The most specific
active policy wins; deterministic safety floors still apply, so configuration
cannot reduce the two-person requirement for confidential or high-risk
documents. Requests persist the selected policy and group, and every decision
rechecks active group membership before exact-candidate publication.

The implementation exposes authenticated admin HTTP APIs for group/member and
policy management. The existing roles remain `admin`, `user`, and `readonly`;
an `owner` is document metadata, not an approval identity. Enterprise IdP group
sync, delegated approval, timed escalation, and approval revocation are not
silently inferred from local groups and remain separate follow-up work.
