# ADR 0009: Federate enterprise identity through a policy-owned principal

## Status

Proposed (2026-08-27)

## Context

The platform currently authenticates local PostgreSQL users with bcrypt and
issues 24-hour HS256 JWTs. Roles map directly to static scopes. This is suitable
for local development and a controlled pilot, but it does not provide enterprise
SSO, centralized deprovisioning, MFA policy, group synchronization, or service
identity. Production middleware also retains compatibility for signed test
tokens whose user ID is absent from PostgreSQL; that behavior must not exist in
an enterprise production profile.

The exact identity provider and provisioning policy have not been selected.

## Decision

Introduce an identity module that returns one policy-owned `Principal` containing
the internal tenant ID, internal subject ID, global role, authentication method,
and granted capabilities. Knowledge-space membership remains authoritative in
the Knowledge Catalog rather than being copied into long-lived browser tokens.

The first enterprise adapter will validate OIDC authorization-code flow tokens
against configured issuer, audience, algorithm, and JWKS. External issuer and
subject pairs map to internal identities. IdP groups are translated through an
explicit tenant-owned mapping; arbitrary role or tenant claims are never trusted
directly.

Provisioning will use SCIM when supported by the chosen provider. A constrained
just-in-time adapter may be added only as a separately reviewed fallback. Service
workloads use distinct client credentials and audiences, not browser sessions.

The Web BFF retains an HttpOnly, Secure, SameSite session cookie. Local login is
disabled in the production profile except for a separately controlled,
audited, time-bounded break-glass procedure. Production rejects unknown or
inactive internal subjects fail closed. Test-token compatibility is limited to
explicit test/evaluation profiles.

## Consequences

- Authentication varies behind one interface while authorization and
  knowledge-space policy retain locality in existing modules.
- Logout, deprovisioning, key rotation, issuer outage, and group-change semantics
  must be specified and tested.
- A staged dual-auth migration is required for existing local users.
- SCIM lifecycle and IdP integration add operational dependencies.

## Open decisions

- Identity provider and supported OIDC/SCIM features.
- External organization-to-tenant mapping and domain ownership proof.
- Required MFA, session lifetime, reauthentication, and logout behavior.
- SCIM-only versus approved just-in-time provisioning fallback.
- Break-glass custody, expiry, alerting, and review process.

## Acceptance gate

No production enablement occurs until the open decisions are approved. Tests
must cover issuer/audience/algorithm confusion, key rotation, deprovisioning,
tenant and group mapping, revoked users, IdP outage, service identity, local
login disablement, and break-glass audit.
