-- External identities are provider-owned identifiers. Authorization remains
-- attached to the current internal user row and is never copied into a binding.
CREATE UNIQUE INDEX users_identity_tenant_key ON users (id, tenant_id);

CREATE TABLE external_identity_bindings (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    issuer TEXT NOT NULL CHECK (issuer <> '' AND issuer = btrim(issuer)),
    external_subject TEXT NOT NULL CHECK (
        external_subject <> '' AND external_subject = btrim(external_subject)
    ),
    internal_user_id UUID NOT NULL,
    tenant_id TEXT NOT NULL,
    created_by TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (issuer, external_subject),
    UNIQUE (id, tenant_id),
    CONSTRAINT external_identity_bindings_user_tenant_fk
        FOREIGN KEY (internal_user_id, tenant_id)
        REFERENCES users (id, tenant_id) ON DELETE CASCADE
);

CREATE INDEX external_identity_bindings_tenant_user_idx
    ON external_identity_bindings (tenant_id, internal_user_id, created_at);
