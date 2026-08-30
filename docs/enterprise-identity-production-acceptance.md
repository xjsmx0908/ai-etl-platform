# Enterprise Identity Production Acceptance

## Status and Scope

Proposed for review (P2.5-E). This document converts the remaining enterprise
identity choices into an approval record and an executable staging gate. It does
not select a production identity provider (IdP), enable SCIM/OIDC, create real
credentials, implement group authorization, JIT, service identity, MFA,
break-glass, or deploy the platform.

The existing deep modules remain authoritative: `oidcauth.Authenticator` owns
OIDC protocol validation, `externalidentity.Directory` resolves exact identity,
and `identitylifecycle.Provisioner` owns the atomic internal lifecycle. A
provider adapter may translate protocol data at those seams, but cannot select
tenant, role, or capabilities.

## Current Platform Contract

Provider preflight must exercise the contract the platform actually implements,
not a generic “SCIM supported” checkbox.

| Interface | Accepted contract | Rejected or deferred |
| --- | --- | --- |
| OIDC | HTTPS discovery whose authorization, token, and JWKS endpoints share the issuer origin; Authorization Code + S256 PKCE; state/nonce; exact issuer and client audience; RS256/JWKS; stable non-empty `sub`; optional client secret sent in the token form | SAML, implicit flow, cross-origin discovery endpoints, provider roles/groups as authority |
| SCIM authentication | Connector-scoped Bearer secret with old/new overlap | Query-string credentials, provider token claims as policy |
| Create | `POST /scim/v2/Users`; exactly the core User schema; non-empty `userName` and `externalId`; send `active` explicitly because omission becomes `false` | Provider roles/groups, schema extensions, inferred subject, tenant, or role |
| Discovery | Exact `GET /scim/v2/Users?filter=userName eq "..."`; `GET /scim/v2/Users/{id}` | Other filter grammar, sorting, pagination extensions, Bulk, `/ServiceProviderConfig`, `/Schemas`, `/ResourceTypes` |
| Replace | `PUT /scim/v2/Users/{id}`; username, display name, primary email, explicit active; optional `externalId` must match | Changing connector, issuer, subject, tenant, or role |
| Patch | PatchOp `replace` for `active` and `displayName`, at most 10 operations | Add/remove and other paths |
| Responses/delete | Platform returns its deterministic UUID `id` and omits `externalId`; `DELETE /scim/v2/Users/{id}` hides the resource while retaining an internal tombstone | Provider dependency on echoed `externalId`, hard deletion, or identifier reuse |
| Limits/errors | Optional bounded `Idempotency-Key` and `If-Match`; maximum configured body size up to 1 MiB; unknown JSON fields rejected; SCIM JSON `400/401/404/405/409/413/503` | Partial acceptance of unsupported input |

Any selected provider must be configurable to this profile or identify the
smallest reviewed compatibility adapter. Do not broaden the core lifecycle
interface merely to mirror a provider-specific payload.

### Known compatibility risks

- Some providers require SCIM discovery endpoints or expect `externalId` to be
  echoed for reconciliation; the current adapter deliberately supports neither.
- Some providers send richer PATCH paths, schema extensions, or omit `active`;
  these requests fail or produce an inactive resource under the current
  contract and must be proven in preflight.
- Some OIDC providers publish token or JWKS endpoints on a different host, or
  require `client_secret_basic`; the current adapter requires same-origin
  endpoints and uses `client_secret_post` when a secret is configured.

Resolve an incompatibility by rejecting the provider, using a narrowly reviewed
external bridge, or approving a separate platform change with protocol tests.
Never compensate by weakening issuer/subject or internal authority invariants.

## Provider Decision Matrix

Score each item from 0 (unsupported) to 5 (verified fit). A product claim is not
evidence; retain configuration exports, official documentation references, and
staging results. Do not total the matrix until the named identity and security
owners approve the weights.

| Criterion | Weight | Microsoft Entra ID | Okta | Keycloak | Required evidence |
| --- | ---: | --- | --- | --- | --- |
| Existing enterprise standard and ownership | 20 | Unscored | Unscored | Unscored | Named owner, tenant/realm, support model |
| OIDC Authorization Code + PKCE interoperability | 15 | Unverified | Unverified | Staging protocol baseline passed | Issuer/client export and acceptance report |
| SCIM Users profile compatibility | 20 | Unverified | Unverified | Unverified | Exact filters, PATCH, PUT, delete, omitted discovery endpoints, and no-echo `externalId` proof |
| Stable OIDC subject available to SCIM | 15 | Unverified | Unverified | Unverified | Create-to-login subject equality evidence |
| MFA, session, logout, and risk policy | 10 | Unverified | Unverified | Unverified | Approved policy and forced-login exercise |
| Credential rotation and audit export | 10 | Unverified | Unverified | Unverified | Rotation run and retained provider audit |
| Availability, support, residency, and cost | 10 | Unverified | Unverified | Unverified | Contract/SLO, residency, support, and budget owner |

Selection rule: reject any candidate scoring below 4 for SCIM compatibility,
stable subject equality, or OIDC interoperability regardless of total score.
Prefer the enterprise's already governed IdP when it passes every mandatory
gate. Keycloak remains the standards baseline, not the default production
choice; any SCIM extension or bridge must be evaluated as a separately operated
dependency.

## Decisions Requiring Approval

Every row is `Pending`; a recommendation is not approval.

| Decision | Recommended starting position | Required owner | Approved value/status |
| --- | --- | --- | --- |
| Production IdP and connector administrator | Select from verified matrix; one accountable administrator | Identity owner | Pending |
| Connector-to-tenant ownership | One connector maps to one existing internal tenant; no domain inference | Tenant owner + security | Pending |
| Stable subject attribute | Provider field must equal the OIDC `sub` exactly; configure as SCIM `externalId` | Identity owner | Pending |
| Initial role | `readonly`; tenant admin promotes internally after business approval | Tenant owner | Pending |
| Stored profile | `userName`, display name, primary email only; no groups/roles | Privacy + tenant owner | Pending |
| Provisioning mode | SCIM-only; JIT stays disabled | Security | Pending |
| Deactivation SLA | End-to-end provider deactivation committed within 5 minutes; platform revocation immediate on receipt | Identity operations | Pending |
| Reconciliation | IdP-authoritative scheduled comparison at least every 24 hours; alert after one missed run; repair only through normal lifecycle commands | Identity operations | Pending; mechanism implemented in P2.5-J |
| Tombstone retention | Retain for the longer of audit retention or account-identifier reuse risk; no automatic purge yet | Legal/security | Pending |
| Credential rotation | 90 days maximum, 24-hour old/new overlap, then explicit retirement | Security operations | Pending |

Blank or rejected decisions keep `OIDC_ENABLED=false` and `SCIM_ENABLED=false`.
Values above are proposals, not silently accepted production policy.

## Staging Acceptance Sequence

Run against an isolated staging tenant and a non-privileged test identity. Use
public OIDC/SCIM interfaces for business observations; database queries may
support diagnostics but cannot prove acceptance.

1. Record provider, tenant/realm, issuer, client, connector, policy version,
   code commit, operator, and UTC start time without secrets.
2. Prove OIDC discovery, Authorization Code + PKCE, exact issuer/audience,
   RS256/JWKS validation and nonce/state single use. Exercise the provider's
   supported signing-key rotation; retain the platform's automated same-`kid`
   rotation test when that provider condition cannot be induced safely.
3. SCIM-create one user with a unique stable `externalId`; verify the returned
   resource and successful OIDC login resolve to the same internal user and
   fixed tenant with the connector's default role.
4. Update `userName`, display name, email, and active state; replay the same
   request concurrently with one explicit shared `Idempotency-Key` and prove one
   identity, one binding, one audit mutation, and no elevation.
5. Deactivate the user; prove an existing platform session fails on its next
   protected request and a fresh OIDC login fails. Measure provider-to-platform
   receipt against the approved SLA.
6. Reactivate only the identical connector/resource/issuer/subject tuple and
   prove OIDC login succeeds without creating a second internal user.
7. Delete the SCIM resource; prove GET/lookup no longer expose it, login fails,
   and reuse of the historical subject or resource cannot take over the account.
8. Rotate SCIM credentials with an approved overlap; prove both work during the
   window and only the new credential works after retirement.
9. Exercise provider, JWKS, Redis, and PostgreSQL outages. Authentication and
   provisioning must fail closed; deactivation retries must not create partial
   state or duplicate audit.
10. Confirm logs, metrics, reports, and audit omit credentials and external
    subjects while retaining connector, operation, result, correlation ID,
    latency, last-success age, and internal user identity.
11. After P2.5-J supplies the approved reconciliation mechanism, create drift in
    both directions: one IdP-disabled/platform-active identity and one
    IdP-absent platform resource. Prove the next scheduled/demand run detects
    both, applies only approved lifecycle transitions, is idempotent on retry,
    records completion age, and alerts when a run is missed.

The current `ai_etl_scim_last_success_unixtime` metric records successful SCIM
mutation activity; it is not evidence that an authoritative full reconciliation
completed. Production remains blocked until P2.5-J adds a distinct reconciliation
completion/failure signal and step 11 passes.

## Evidence and Promotion Gate

Retain one secret-free, immutable acceptance bundle containing:

- signed decision matrix and all owner approvals;
- sanitized provider configuration/version and exact SCIM profile;
- test timestamps, correlation IDs, assertions, and latency/SLA results;
- negative-path, outage, rotation, and identifier-reuse evidence;
- platform commit and image digests plus rollback owner and expiry date.

Use [enterprise-identity-acceptance.template.json](enterprise-identity-acceptance.template.json)
as the public-safe index/manifest for the private bundle, not as the bundle
itself. Each evidence reference records only an approved private-system object
ID, digest, outcome, and timestamps; signatures record their mechanism and
detached-signature digest. Store the completed artifacts in an approved private
evidence system. Do not commit real tenant aliases, personal names, request
payloads, or configuration exports to this repository.

Security and identity owners must sign the same bundle. Evidence expires after
90 days, a provider/profile change, subject-mapping change, or relevant platform
identity change. Promotion is blocked if any mandatory test is skipped, any
secret/subject leaks, the provider requires tenant/role inference, or rollback
has not been rehearsed.

## Subsequent Reviewed Slices

P2.5-E design ends with a reviewable decision record and repeatable acceptance
plan. Provider selection occurs only after the named enterprise owners supply
and approve its evidence. Later changes remain separate so each module keeps
one policy responsibility:

1. P2.5-F：拟议的 MFA、会话寿命、重新认证、RP 发起退出和双认证迁移契约见
   [企业会话安全设计](enterprise-session-security-design.md)；企业责任人仍须
   批准每个 `Pending` 值。
2. P2.5-G：拟议的可审计、限时紧急访问保管、事故租约和演练契约见
   [企业紧急访问设计](enterprise-emergency-access-design.md)；企业责任人仍须
   批准所有 `Pending` 值，且 P2.5-J 必须引用有效演练证据。
3. P2.5-H：拟议的租户企业组到知识空间授权、完整快照和撤权契约见
   [企业组到知识空间授权设计](enterprise-group-authorization-design.md)；raw
   provider group 永远不能推导租户或平台角色，P2.5-J 必须保留真实同步证据。
4. P2.5-I: workload/service identity with distinct audiences, credentials,
   capabilities, rotation, and no browser session reuse.
5. P2.5-J: selected-provider adapter/configuration, authoritative reconciliation
   with distinct completion evidence, and the staging acceptance run. Production
   enablement and deployment require a later explicit approval.
