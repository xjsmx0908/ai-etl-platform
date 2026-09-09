# Product Requirements Document

## Product Goal

Turn the current AI ETL/RAG platform into a governed enterprise knowledge
product where users understand which knowledge space supplied an answer and
administrators control what is eligible for use.

## Current Delivery Goal

P2.4-R1 is complete: a genuinely autonomous, read-only Agent pre-review loop is
implemented, integrated with Knowledge Release Center, and accepted with the
deterministic matrix plus real-model scenarios. P2.4-R5 is complete: durable approval notifications can reach WeCom/DingTalk and
an optional external workflow webhook, and reverse compensation covers
side-effecting Agent tools. Broader compliance classification, external policy
retrieval, and embedded BPM engines remain later scope. Report TTL, expiry,
automatic rereview, and retention cleanup are delivered by P2.4-R3.

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

## Product Experience Acceptance

In addition to isolation tests, a reviewer must walk the live workbench as
reader, contributor, and administrator. Visual issues, confusing controls, and
functional defects are logged in the findings register defined by
[`product-experience-acceptance.md`](product-experience-acceptance.md).

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

In the first release, “Agent pre-review” is a bounded autonomous review loop
on the existing Agent Orchestrator. The model observes persisted tool results,
chooses the next action from four registered read-only tools, and submits a
structured recommendation with exact-candidate evidence. Tenant, document,
candidate, permissions, budgets, approvals, and publication remain controlled
by the server. Deterministic sensitive-data and prompt-injection findings can
only be preserved or escalated, never removed or downgraded by the model.

P2.4-R1 has implemented this minimum autonomous loop, including strict
status/recommendation/risk/findings validation, chunk evidence checks,
candidate consistency checks, and fail-closed handling. The Review Agent
directly reuses the RAG Query model endpoint, credentials, and model. The
deterministic 10-scenario release-center matrix, token-budget termination,
Review Agent recovery evidence, and real-model four-scenario acceptance pass.
This bounded review is not a complete semantic, privacy, or compliance classifier.
P2.4-R6 adds knowledge-space purpose and a fitness observation: whether the
document belongs in this space and can be used as formal knowledge. Optional
kind labels are human notes only and never a publish switch. Version diff,
policy libraries, and cross-document conflict checks remain out of scope.

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

### P2.4-R5 orchestration around Agent review

Release-center request open, decision, reject, publish, and stale-candidate
events write a tenant-scoped notification outbox. Payloads contain identifiers
and enums only. Delivery is asynchronous, idempotent, and fail-open. Agent runs
compensate completed side-effecting steps in reverse when a later tool, timeout,
cancellation, or rejection fails the run. External workflow engines consume the
same webhook contract and submit decisions through
`POST /v1/release-center/workflow/decision`, which resolves an active tenant
administrator and reuses the existing idempotent approval API. The platform
does not embed a BPM engine.

### P2.4-R3 review report lifecycle

P2.4-R3 assigns a server-side TTL to stored review reports (default 7 days;
`RELEASE_REVIEW_TTL=0` disables expiry). Expired pending reports move to
`needs_info` and cannot be used to approve publication. The same exact
candidate is automatically rereviewed with a new report ID while the expired
row remains as audit evidence. Published or rejected requests keep their
original report status. Expired unreferenced reports may be purged after
`RELEASE_REVIEW_RETENTION` (default 90 days).
