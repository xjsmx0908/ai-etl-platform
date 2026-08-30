# Enterprise Session Security Design

## Status and Scope

Proposed for review (P2.5-F). This design defines the enterprise MFA, session,
reauthentication, logout, and dual-auth migration contract. It does not select
or configure an identity provider (IdP), set production credentials, implement
break-glass access, enable federation, or deploy the platform. Every numeric
policy below remains `Pending` until the named enterprise owner approves it.

## Verified Current State

The platform issues HS256 JWTs with a fixed 24-hour lifetime and stores them in
the Web BFF's `ai_etl_token` HttpOnly, SameSite=Lax cookie. Tokens identify the
authentication method and user `token_version`; protected requests re-read the
current PostgreSQL user, so deactivation or a version increment revokes all of
that user's tokens on the next request.

There is no unique session ID, session registry, idle timeout, assurance or
`auth_time` state, privileged-action reauthentication, or per-device
revocation. Logout only expires the browser cookie; the JWT remains valid until
expiry or user-wide revocation. OIDC currently requests only `openid` and
validates identity protocol claims, but does not parse or retain `acr`, `amr`,
or `auth_time`. Production with OIDC enabled rejects password login; staging
allows local and federated sessions for migration.

## Security Invariants

1. The IdP enforces enrollment and authentication factors. The platform still
   fails closed unless the selected provider adapter supplies approved,
   provider-neutral assurance and a trustworthy authentication time.
2. Role, group, tenant, email, and login success never imply MFA. Internal
   PostgreSQL authority remains the source of tenant, role, and capabilities.
3. An IdP outage cannot prevent local session revocation. A session-store
   outage in production denies access rather than accepting an unchecked JWT.
4. Provider tokens and raw assurance claims never reach business handlers or
   browser JavaScript. Logs and audit events contain policy outcomes and stable
   internal IDs, not tokens or raw external subjects.
5. Idle expiry, absolute expiry, authentication freshness, IdP session lifetime,
   and cookie lifetime are separate controls with separately tested semantics.

## Session Policy Seam

Add a deep `session` module behind a small, provider-neutral interface. Its
conceptual operations are `Establish(principal, evidence)`,
`Authenticate(credential, action)`, and `Revoke(credential, scope)`. The module
owns session state, expiry, assurance, rotation, revocation, and audit decisions.
`Authenticate` returns an internal principal plus one of `allow`, `deny`, or
`reauthenticate`; it never returns provider claims.

The OIDC adapter translates the selected provider's exact `acr`, `amr`, and
`auth_time` semantics into an approved `AuthenticationEvidence` value. This
translation is configuration reviewed in P2.5-J, not a generic claim allowlist.
HTTP route registration maps each action to a policy-owned risk class. Handlers
therefore ask for an action such as `identity.binding.change` and do not know
which IdP or authentication method satisfied it.

## Durable Session Model

Replace acceptance of a self-contained browser JWT with a registry-backed
platform credential. A signed credential may remain during migration, but it
must contain a unique, unguessable session ID and every production request must
resolve that ID through the session module. The durable record contains:

- session ID, internal tenant/user IDs, and authentication method;
- created, authenticated, last-seen, absolute-expiry, and revoked timestamps;
- provider-neutral assurance level and methods, plus policy version;
- revocation reason, credential rotation generation, and minimal audit
  correlation; and
- no provider access token, refresh token, password, raw external subject, or
  authorization snapshot.

Mutable role and capabilities continue to come from the user and knowledge
catalog stores. Last-seen persistence may be coalesced, but the documented
maximum write interval must not extend the approved idle timeout. Establishment
and successful reauthentication rotate the credential to prevent fixation.

## Policy Decisions Requiring Approval

| Decision | Required owner | Approved value/status |
| --- | --- | --- |
| Accepted MFA assurance and factor combinations per IdP | Security + identity | Pending |
| Ordinary-session idle timeout | Security + product | Pending |
| Ordinary-session absolute lifetime | Security + product | Pending |
| Privileged authentication freshness | Security | Pending |
| Cookie lifetime and renewal behavior | Security | Pending |
| Concurrent-session/device limit | Security + support | Pending |
| User self-service session visibility/revocation | Product + privacy | Pending |
| RP-initiated and optional back-channel logout profile | Identity + security | Pending |
| Dual-auth cohort, observation period, and cutover date | Identity + operations | Pending |

Blank decisions block P2.5-J staging acceptance and production enablement. The
current 24-hour value is observed behavior, not an approved enterprise default.

## Reauthentication and High-Risk Actions

At minimum, identity/binding administration, role changes, publication
approval, governed deletion, agent execution approval, credential rotation,
and session-wide revocation require a reviewed risk classification. A high-risk
request without sufficiently fresh approved assurance returns
`reauthenticate`, not `forbidden` and not a silent redirect from an API call.

The Web BFF starts a new state/nonce/PKCE transaction bound server-side to the
current session, intended action, and safe return path. The provider adapter
requests fresh authentication and the required assurance using only mechanisms
verified for that IdP. Callback completion must prove transaction single use,
the same internal subject, approved evidence, and freshness before rotating the
platform session. Subject changes terminate the flow. Missing or ambiguous
`auth_time`/assurance evidence fails closed for the privileged action.

State-changing Web routes must also enforce an explicit same-origin/CSRF
control; SameSite cookies alone are not the complete privileged-action defense.

## Logout and Revocation

Logout order is fixed: atomically revoke the platform session with its audit
outcome, expire the cookie, then optionally redirect through the selected
provider's validated RP-initiated logout endpoint. Provider timeout or failure
cannot undo local revocation or block the local completion response.
Post-logout redirects use a configured same-origin allowlist and single-use
state.

The interface distinguishes current-session logout, user-requested other-device
revocation, and security/admin all-session revocation. User deactivation and
credential compromise retain the existing user-wide `token_version` kill
switch. Whether an encrypted, TTL-bounded ID-token hint is needed for provider
logout is a P2.5-J adapter decision; it is never session authority and is never
returned to client JavaScript.

## Dual-Auth Migration and Rollback

1. Inventory active local users, owners, last use, role, tenant, and external
   binding status without exporting password hashes or personal data.
2. Bind exact `(issuer, subject)` identities through the reviewed lifecycle;
   never auto-link by email, display name, domain, or group.
3. Migrate named staging and tenant cohorts. Measure federated success/failure,
   reauthentication, logout, deactivation latency, and help-desk recovery for
   the approved observation period.
4. Block new password creation, prove every in-scope administrator has a tested
   federated identity, then disable production password login and invalidate
   remaining local sessions.
5. Retain a signed cohort/cutover record. Rollback disables the faulty federated
   entry point and follows an approved recovery runbook; it must not silently
   reactivate passwords, reset user lifecycle ownership, or bypass MFA.

Emergency access is intentionally deferred to P2.5-G. It cannot be implemented
as an undocumented exception to the migration or session policy.

## Acceptance and Delivery Slices

Implementation should proceed as separately reviewed slices: schema and session
module; registry-backed authentication and expiry; assurance translation and
reauthentication; local and RP-initiated logout; then cohort migration tooling.
Each slice must preserve default-off OIDC and local deterministic evaluation.

Acceptance tests cover idle and absolute boundaries, clock skew, credential
rotation, fixation and replay, stale assurance, wrong-subject reauthentication,
per-session versus all-session revocation, concurrent requests, store/IdP
outages, CSRF, safe redirects, audit redaction, deactivation, and rollback.
P2.5-J must additionally retain selected-provider evidence for MFA enforcement,
fresh login, logout, lifecycle revocation, and dual-auth cutover rehearsal.
