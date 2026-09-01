ALTER TABLE platform_sessions
    ADD COLUMN management_handle TEXT,
    ADD COLUMN creation_order BIGINT;

CREATE SEQUENCE platform_sessions_creation_order_seq;

WITH ordered AS (
    SELECT id, row_number() OVER (ORDER BY created_at, id) AS position
    FROM platform_sessions
)
UPDATE platform_sessions AS sessions
SET creation_order = ordered.position
FROM ordered
WHERE sessions.id = ordered.id;

SELECT setval(
    'platform_sessions_creation_order_seq',
    COALESCE((SELECT max(creation_order) FROM platform_sessions), 0) + 1,
    false
);

UPDATE platform_sessions
SET management_handle = 'sm1_' || gen_random_uuid()::text
WHERE management_handle IS NULL;

ALTER TABLE platform_sessions
    ALTER COLUMN management_handle SET NOT NULL,
    ALTER COLUMN creation_order SET DEFAULT nextval('platform_sessions_creation_order_seq'),
    ALTER COLUMN creation_order SET NOT NULL,
    ADD CONSTRAINT platform_sessions_management_handle_format CHECK (
        management_handle ~ '^sm1_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
    ),
    ADD CONSTRAINT platform_sessions_management_handle_unique UNIQUE (management_handle),
    ADD CONSTRAINT platform_sessions_creation_order_unique UNIQUE (creation_order);

ALTER SEQUENCE platform_sessions_creation_order_seq OWNED BY platform_sessions.creation_order;
