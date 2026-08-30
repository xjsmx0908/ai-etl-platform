-- Provider-neutral provisioning ownership. Connector policy fixes tenant and
-- least-privilege authority before any SCIM request is accepted.
ALTER TABLE users
    ADD COLUMN origin TEXT NOT NULL DEFAULT 'local'
        CHECK (origin IN ('local', 'scim')),
    ADD COLUMN display_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN email TEXT NOT NULL DEFAULT '';

CREATE TABLE identity_provisioning_connectors (
    id TEXT PRIMARY KEY CHECK (id <> '' AND id = btrim(id)),
    tenant_id TEXT NOT NULL REFERENCES tenants(id),
    issuer TEXT NOT NULL CHECK (issuer <> '' AND issuer = btrim(issuer)),
    subject_attribute TEXT NOT NULL CHECK (subject_attribute <> ''),
    default_role TEXT NOT NULL DEFAULT 'readonly'
        CHECK (default_role IN ('readonly', 'user')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id, tenant_id)
);

CREATE TABLE identity_lifecycle_resources (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    connector_id TEXT NOT NULL,
    provider_resource_id TEXT NOT NULL CHECK (
        provider_resource_id <> '' AND provider_resource_id = btrim(provider_resource_id)
    ),
    internal_user_id UUID NOT NULL,
    tenant_id TEXT NOT NULL,
    issuer TEXT NOT NULL,
    external_subject TEXT NOT NULL,
    source_version TEXT NOT NULL DEFAULT '',
    deleted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (connector_id, provider_resource_id),
    UNIQUE (internal_user_id),
    FOREIGN KEY (connector_id, tenant_id)
        REFERENCES identity_provisioning_connectors (id, tenant_id),
    FOREIGN KEY (internal_user_id, tenant_id)
        REFERENCES users (id, tenant_id),
    FOREIGN KEY (issuer, external_subject)
        REFERENCES external_identity_bindings (issuer, external_subject)
);

CREATE INDEX identity_lifecycle_resources_connector_updated_idx
    ON identity_lifecycle_resources (connector_id, updated_at DESC);

CREATE TABLE identity_lifecycle_idempotency (
    connector_id TEXT NOT NULL REFERENCES identity_provisioning_connectors(id),
    idempotency_key TEXT NOT NULL CHECK (
        idempotency_key <> '' AND idempotency_key = btrim(idempotency_key)
    ),
    request_hash TEXT NOT NULL,
    response JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (connector_id, idempotency_key)
);
