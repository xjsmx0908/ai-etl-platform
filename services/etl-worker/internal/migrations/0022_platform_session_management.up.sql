ALTER TABLE platform_sessions
    ADD COLUMN management_handle TEXT;

UPDATE platform_sessions
SET management_handle = 'sm1_' || gen_random_uuid()::text
WHERE management_handle IS NULL;

ALTER TABLE platform_sessions
    ALTER COLUMN management_handle SET NOT NULL,
    ADD CONSTRAINT platform_sessions_management_handle_format CHECK (
        management_handle ~ '^sm1_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    ),
    ADD CONSTRAINT platform_sessions_management_handle_unique UNIQUE (management_handle);
