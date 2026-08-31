-- Platform sessions persist authentication state only. Current authorization
-- remains in users and knowledge-space stores and is resolved after this seam.
CREATE TABLE platform_session_subject_states (
    internal_user_id UUID NOT NULL,
    tenant_id TEXT NOT NULL,
    revoked_before TIMESTAMPTZ NOT NULL,
    revoked_correlation_id TEXT NOT NULL CHECK (
        revoked_correlation_id <> '' AND
        char_length(revoked_correlation_id) <= 256 AND
        revoked_correlation_id = btrim(revoked_correlation_id)
    ),
    PRIMARY KEY (tenant_id, internal_user_id),
    FOREIGN KEY (internal_user_id, tenant_id)
        REFERENCES users (id, tenant_id) ON DELETE CASCADE
);

CREATE TABLE platform_sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    credential_digest BYTEA NOT NULL UNIQUE
        CHECK (octet_length(credential_digest) = 32),
    internal_user_id UUID NOT NULL,
    tenant_id TEXT NOT NULL,
    authentication_method TEXT NOT NULL CHECK (
        authentication_method IN ('local', 'federated')
    ),
    assurance_level TEXT NOT NULL CHECK (
        assurance_level <> '' AND assurance_level = btrim(assurance_level)
    ),
    authenticated_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    last_activity_at TIMESTAMPTZ NOT NULL,
    absolute_expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    revocation_reason TEXT NOT NULL DEFAULT '',
    established_correlation_id TEXT NOT NULL,
    revoked_correlation_id TEXT NOT NULL DEFAULT '',
    generation BIGINT NOT NULL DEFAULT 1 CHECK (generation > 0),
    policy_revision TEXT NOT NULL CHECK (
        policy_revision <> '' AND policy_revision = btrim(policy_revision)
    ),
    FOREIGN KEY (internal_user_id, tenant_id)
        REFERENCES users (id, tenant_id) ON DELETE CASCADE,
    CHECK (authenticated_at <= last_activity_at),
    CHECK (last_activity_at < absolute_expires_at),
    CHECK (established_correlation_id <> '' AND char_length(established_correlation_id) <= 256 AND
           established_correlation_id = btrim(established_correlation_id)),
    CHECK (char_length(revoked_correlation_id) <= 256 AND
           revoked_correlation_id = btrim(revoked_correlation_id)),
    CHECK ((revoked_at IS NULL AND revocation_reason = '' AND revoked_correlation_id = '') OR
           (revoked_at IS NOT NULL AND revocation_reason <> '' AND revoked_correlation_id <> ''))
);

CREATE INDEX platform_sessions_subject_active_idx
    ON platform_sessions (tenant_id, internal_user_id, absolute_expires_at)
    WHERE revoked_at IS NULL;
