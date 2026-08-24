-- First-class knowledge spaces replace free-form retrieval scope as the source
-- of truth. IDs are tenant-local stable slugs because every retrieval already
-- carries tenant_id; this also lets legacy knowledge_base_id values backfill
-- without rewriting indexed chunks before deployment.
CREATE TABLE knowledge_spaces (
    tenant_id   TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    id          TEXT NOT NULL,
    name        TEXT NOT NULL,
    kind        TEXT NOT NULL DEFAULT 'production'
                CHECK (kind IN ('production','demo')),
    is_default  BOOLEAN NOT NULL DEFAULT FALSE,
    active      BOOLEAN NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id)
);
CREATE UNIQUE INDEX knowledge_spaces_one_default_idx
    ON knowledge_spaces (tenant_id) WHERE is_default AND active;

-- Every tenant gets one deterministic production default, including tenants
-- without documents. Existing production queries therefore remain available.
INSERT INTO knowledge_spaces (tenant_id, id, name, kind, is_default)
SELECT id, 'user-uploads', '用户上传', 'production', TRUE FROM tenants;

-- Preserve every legacy knowledge-base slug as a non-default governed space.
INSERT INTO knowledge_spaces (tenant_id, id, name, kind, is_default)
SELECT DISTINCT
    d.tenant_id,
    COALESCE(NULLIF(btrim(d.metadata->>'knowledge_base_id'), ''), 'user-uploads') AS id,
    CASE
        WHEN d.metadata->>'knowledge_base_id' = 'enterprise-demo' THEN '演示知识库'
        ELSE COALESCE(NULLIF(btrim(d.metadata->>'knowledge_base_id'), ''), '用户上传')
    END AS name,
    CASE WHEN d.metadata->>'knowledge_base_id' = 'enterprise-demo' THEN 'demo' ELSE 'production' END AS kind,
    FALSE
FROM documents d
WHERE COALESCE(NULLIF(btrim(d.metadata->>'knowledge_base_id'), ''), 'user-uploads') <> 'user-uploads'
ON CONFLICT (tenant_id, id) DO NOTHING;

CREATE TABLE knowledge_space_members (
    tenant_id  TEXT NOT NULL,
    space_id   TEXT NOT NULL,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       TEXT NOT NULL CHECK (role IN ('reader','contributor','manager')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, space_id, user_id),
    FOREIGN KEY (tenant_id, space_id)
        REFERENCES knowledge_spaces(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX knowledge_space_members_user_idx
    ON knowledge_space_members (tenant_id, user_id);

-- Users provisioned after this migration inherit the same default-space role
-- mapping as existing users. Tenant admins still have tenant-wide bypass.
CREATE FUNCTION add_default_knowledge_space_membership() RETURNS trigger AS $$
BEGIN
    IF NEW.active THEN
        INSERT INTO knowledge_space_members (tenant_id, space_id, user_id, role)
        VALUES (NEW.tenant_id, 'user-uploads', NEW.id,
                CASE NEW.role WHEN 'admin' THEN 'manager' WHEN 'user' THEN 'contributor' ELSE 'reader' END)
        ON CONFLICT (tenant_id, space_id, user_id) DO UPDATE SET role = EXCLUDED.role;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER users_default_knowledge_space_membership
AFTER INSERT OR UPDATE OF role, active ON users
FOR EACH ROW EXECUTE FUNCTION add_default_knowledge_space_membership();

-- Preserve current product capabilities in the default production space.
INSERT INTO knowledge_space_members (tenant_id, space_id, user_id, role)
SELECT tenant_id, 'user-uploads', id,
       CASE role WHEN 'admin' THEN 'manager' WHEN 'user' THEN 'contributor' ELSE 'reader' END
FROM users WHERE active;

ALTER TABLE documents
    ADD COLUMN knowledge_space_id TEXT,
    ADD COLUMN publication_status TEXT NOT NULL DEFAULT 'draft'
        CHECK (publication_status IN ('draft','published','retired'));

UPDATE documents
SET knowledge_space_id = COALESCE(
        NULLIF(btrim(metadata->>'knowledge_base_id'), ''),
        'user-uploads'
    ),
    publication_status = CASE
        WHEN status = 'completed'
         AND doc_status = 'active'
         AND COALESCE(NULLIF(btrim(metadata->>'knowledge_base_id'), ''), 'user-uploads') <> 'enterprise-demo'
        THEN 'published'
        WHEN doc_status IN ('superseded','archived') THEN 'retired'
        ELSE 'draft'
    END;

ALTER TABLE documents
    ALTER COLUMN knowledge_space_id SET NOT NULL,
    ALTER COLUMN knowledge_space_id SET DEFAULT 'user-uploads',
    ADD CONSTRAINT documents_knowledge_space_fk
        FOREIGN KEY (tenant_id, knowledge_space_id)
        REFERENCES knowledge_spaces(tenant_id, id);

CREATE INDEX documents_tenant_space_publication_idx
    ON documents (tenant_id, knowledge_space_id, publication_status);
CREATE INDEX documents_tenant_space_hash_idx
    ON documents (tenant_id, knowledge_space_id, file_hash);
