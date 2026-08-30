# Enterprise Identity Lifecycle Design

## Status and Scope

Implemented and locally verified (P2.5-D); production remains disabled pending
selected-provider acceptance. This design adds provider-neutral account
provisioning and deprovisioning after the OIDC foundation. It does not select a
production IdP, enable federation, infer authorization from provider claims, or
implement group mapping, service identities, MFA, or break-glass access.

SCIM 2.0 is the preferred adapter. Just-in-time (JIT) creation remains disabled
unless separately approved; manual external-identity bindings remain the safe
migration path.

## Authority and Invariants

- Each provisioning connector is configured for exactly one internal tenant.
  Provider organization names, email domains, and token claims cannot choose a
  tenant.
- A connector may create users only with its configured least-privilege default
  role, initially `readonly`. SCIM roles and groups are stored as neither roles
  nor capabilities; a later tenant-owned group-mapping module may change them.
- The adapter must produce an exact OIDC `(issuer, subject)` identity using an
  explicitly configured provider attribute. It must never guess from email or
  `userName`.
- Deactivation is authoritative and immediate: one PostgreSQL transaction sets
  `users.active=false`, increments `token_version`, preserves the external
  binding, and appends an audit record. Existing platform sessions then fail on
  their next request.
- Delete means a retained tombstone, not hard deletion. This prevents a recycled
  provider identifier from silently taking over historical access or audit.
- Reactivation is permitted only for the same connector, provider resource ID,
  issuer, subject, and internal user. Conflicts fail closed.

## Deep Module and Data Ownership

`internal/identitylifecycle` owns one provisioning seam:

```go
type Provisioner interface {
    Apply(context.Context, LifecycleCommand) (LifecycleResult, error)
}
```

`LifecycleCommand` carries the connector identity, provider resource ID,
normalized external identity, desired active state, display attributes, source
version, and idempotency key. Callers do not coordinate users, bindings,
revocation, tombstones, or audit separately; the PostgreSQL adapter commits them
atomically. Replaying the same key and request hash returns the prior result;
reusing a key with different input returns a conflict.

New tables record connector-to-tenant policy and provisioned-resource ownership.
The user row gains an origin and optional display attributes. Federated-only
users have no usable password credential; local login explicitly rejects them.
Raw SCIM payloads, access tokens, group lists, and unnecessary profile data are
not retained.

## SCIM Adapter

The implemented adapter exposes the required subset of `/scim/v2/Users`: filtered
lookup, create, replace, patch, deactivate, and delete. Unsupported filters,
bulk operations, schemas, or mutable tenant/role attributes return SCIM errors
rather than being partially applied. Authentication uses a hashed, rotatable,
connector-scoped bearer credential loaded from a secret file. Request bodies,
responses, logs, and metrics are bounded and redact credentials and external
subjects.

The adapter translates SCIM protocol details into `LifecycleCommand`; it does
not write identity tables directly. Provider-specific attribute extraction is
configuration owned by the connector and is acceptance-tested against the
selected production provider before enablement.

## Failure, Audit, and Operations

- Database or audit failure rolls back the complete lifecycle mutation.
- Conflicting subject, provider resource, username, or idempotency ownership
  returns `409` without changing either account.
- Deprovisioning remains available when the OIDC provider is unavailable because
  PostgreSQL, not live discovery, owns authorization state.
- Audit records include connector, tenant, internal user, operation, result, and
  correlation ID, but omit bearer credentials and external subjects.
- Metrics expose bounded counts and latency by operation/result/connector, plus
  last-success age. Alerts cover sustained failures and stale synchronization.
- Connector disablement stops new writes but does not reactivate users or erase
  tombstones. Secret rotation supports overlap followed by explicit retirement.

## Test Seams and Acceptance

Implementation uses red/green slices at two reviewable seams:

1. `Provisioner.Apply` with real PostgreSQL: create/replay, collision rollback,
   atomic audit, deactivate/session revocation, retained tombstone, constrained
   reactivation, tenant isolation, and dependency failure.
2. SCIM HTTP behavior: authentication, supported filters and PATCH operations,
   schema/error responses, payload bounds, redaction, and concurrent replay.

Local acceptance covers atomic PostgreSQL create/replay/update/deactivate/
delete/reactivate behavior and the bounded SCIM HTTP contract. Production
acceptance still requires a real selected-provider create/update/deactivate/
reactivate run proving that OIDC login succeeds only while the internal user is
active. Production remains blocked until the decisions below are approved.

## Required Enterprise Decisions

1. Production IdP and its supported SCIM profile.
2. Connector-to-tenant ownership and administrator.
3. Exact provider attribute that carries the stable OIDC subject.
4. Default role (`readonly` recommended) and profile fields permitted to store.
5. SCIM-only lifecycle versus a separately constrained JIT fallback.
6. Deactivation SLA, tombstone retention, reconciliation interval, and credential
   rotation policy.
