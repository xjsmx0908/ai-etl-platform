-- Repair tenants left behind by a partially failed bootstrap and ensure every
-- future tenant has its default space before a user membership can reference it.
INSERT INTO knowledge_spaces (tenant_id, id, name, kind, is_default, active)
SELECT id, 'user-uploads', '用户上传', 'production', TRUE, TRUE
FROM tenants
ON CONFLICT (tenant_id, id) DO NOTHING;

CREATE FUNCTION add_default_knowledge_space() RETURNS trigger AS $$
BEGIN
    INSERT INTO knowledge_spaces
        (tenant_id, id, name, kind, is_default, active)
    VALUES
        (NEW.id, 'user-uploads', '用户上传', 'production', TRUE, TRUE)
    ON CONFLICT (tenant_id, id) DO NOTHING;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER tenants_default_knowledge_space
AFTER INSERT ON tenants
FOR EACH ROW EXECUTE FUNCTION add_default_knowledge_space();
