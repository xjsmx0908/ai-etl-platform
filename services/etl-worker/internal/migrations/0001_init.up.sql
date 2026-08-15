-- Tenants: id is a short slug (e.g. 'default', 'acme'). Phase 1 has no tenant
-- self-service; admins provision tenants via API or the bootstrap seed.
CREATE TABLE tenants (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Users: globally unique username; role drives document visibility and scopes.
CREATE TABLE users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username      TEXT NOT NULL,
    password_hash TEXT NOT NULL,                              -- bcrypt
    role          TEXT NOT NULL CHECK (role IN ('admin','user','readonly')),
    tenant_id     TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    active        BOOLEAN NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_username_key ON users (lower(username));

-- Documents registry: ONE row per uploaded document, tenant-scoped. doc_id is
-- only unique per tenant. status/stage/chunks_* mirror model.TaskStatus and are
-- written through by the worker so the registry survives the 7-day Redis TTL.
CREATE TABLE documents (
    id           BIGSERIAL PRIMARY KEY,
    tenant_id    TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    doc_id       TEXT NOT NULL,
    file_name    TEXT NOT NULL DEFAULT '',
    object_key   TEXT NOT NULL DEFAULT '',
    file_hash    TEXT NOT NULL DEFAULT '',
    file_size    BIGINT NOT NULL DEFAULT 0,
    content_type TEXT NOT NULL DEFAULT '',
    permission   TEXT NOT NULL DEFAULT 'internal'
                 CHECK (permission IN ('public','internal','confidential')),
    status       TEXT NOT NULL DEFAULT 'queued'
                 CHECK (status IN ('queued','processing','completed','failed')),
    stage        TEXT NOT NULL DEFAULT 'queued',
    chunks_done  INT  NOT NULL DEFAULT 0,
    chunks_total INT  NOT NULL DEFAULT 0,
    error        TEXT NOT NULL DEFAULT '',
    metadata     JSONB NOT NULL DEFAULT '{}',
    uploaded_by  TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX documents_tenant_doc_key ON documents (tenant_id, doc_id);
CREATE INDEX documents_tenant_status_idx    ON documents (tenant_id, status);
CREATE INDEX documents_tenant_perm_idx      ON documents (tenant_id, permission);
CREATE INDEX documents_tenant_created_idx   ON documents (tenant_id, created_at DESC);
