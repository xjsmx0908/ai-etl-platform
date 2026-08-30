# ADR 0009: Federate enterprise identity through a policy-owned principal

## Status

Partially accepted (2026-08-30)

## Context

The platform currently authenticates local PostgreSQL users with bcrypt and
issues 24-hour HS256 JWTs. Roles map directly to static scopes. This is suitable
for local development and a controlled pilot, but it does not provide enterprise
SSO, centralized deprovisioning, MFA policy, group synchronization, or service
identity. Production middleware also retains compatibility for signed test
tokens whose user ID is absent from PostgreSQL; that behavior must not exist in
an enterprise production profile.

The provider-neutral OIDC protocol and Keycloak as the first acceptance target
are approved. The production provider, organization-to-tenant policy,
provisioning ownership, MFA/session policy, and break-glass process remain
unselected.

## Decision

Introduce an identity module that returns one policy-owned `Principal` containing
the internal tenant ID, internal subject ID, global role, authentication method,
and granted capabilities. Knowledge-space membership remains authoritative in
the Knowledge Catalog rather than being copied into long-lived browser tokens.

The first enterprise adapter validates OIDC authorization-code flow tokens
against configured issuer, audience, algorithm, and JWKS. External issuer and
subject pairs map to internal identities. Arbitrary role, tenant, email-domain,
or group claims are never trusted as authorization. Group mapping remains a
separately reviewed future adapter.

Provisioning will use SCIM when supported by the chosen provider. A constrained
just-in-time adapter may be added only as a separately reviewed fallback. Service
workloads use distinct client credentials and audiences, not browser sessions.
The proposed lifecycle module, authority rules, deprovisioning semantics, and
remaining enterprise inputs are detailed in
[Enterprise Identity Lifecycle Design](../identity-lifecycle-design.md).
Provider selection, policy approval, and the real staging evidence contract are
detailed in
[Enterprise Identity Production Acceptance](../enterprise-identity-production-acceptance.md).
拟议的 MFA、会话、重新认证、退出和迁移契约详见
[企业会话安全设计](../enterprise-session-security-design.md)。
拟议的紧急访问保管、租约、最小权限和演练契约详见
[企业紧急访问设计](../enterprise-emergency-access-design.md)。

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
- Browser transactions use PKCE, state, and nonce. Non-development replicas
  share single-use transaction state through the durable Redis instance; IdP
  tokens are not persisted there or returned to browser JavaScript.

## Open decisions

- Production identity provider and supported SCIM features. Keycloak is only
  the first standards-compatibility acceptance target.
- External organization-to-tenant mapping and domain ownership proof.
- 批准 P2.5-F 的 MFA 证据、会话寿命、重新认证、退出和双认证迁移值；所有
  数值仍为 `Pending`。
- SCIM-only versus approved just-in-time provisioning fallback.
- 批准 P2.5-G 的独立保管、多方审批、租约到期、最小恢复动作、告警、演练和
  复盘策略；所有人数与数值仍为 `Pending`。

## Acceptance gate

No production enablement occurs until the open decisions are approved. Tests
must cover issuer/audience/algorithm confusion, key rotation, deprovisioning,
tenant and group mapping, revoked users, IdP outage, service identity, local
login disablement, and break-glass audit.

P2.5-E does not resolve the open decisions. It defines the comparison evidence,
named approvals, expiry rules, and staging lifecycle sequence required to close
them. Keycloak's completed OIDC protocol run is baseline evidence only and does
not select it as the production IdP or prove a production SCIM profile.

P2.5-F 提议与提供方无关的会话策略接缝和持久会话注册表，但不批准策略值，
也不实现生产身份。P2.5-G 单独负责紧急访问；P2.5-J 必须绑定选定提供方的
精确认认证保证与退出行为，并保留 staging 证据。

P2.5-G 提议独立 `emergencyaccess` 模块、无常驻权限的短期事故租约和失败关闭
审计，但不创建账号、凭据或生产入口。基础设施灾难恢复不由应用紧急访问绕过；
P2.5-J 必须引用仍有效的紧急访问演练证据。
