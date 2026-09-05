-- Tenant-scoped approval groups and deterministic release-center policies.
CREATE TABLE release_center_approval_groups (
    tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    group_id TEXT NOT NULL,
    name TEXT NOT NULL,
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, group_id)
);

CREATE TABLE release_center_approval_group_members (
    tenant_id TEXT NOT NULL,
    group_id TEXT NOT NULL,
    user_id UUID NOT NULL,
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, group_id, user_id),
    FOREIGN KEY (tenant_id, group_id)
        REFERENCES release_center_approval_groups (tenant_id, group_id) ON DELETE CASCADE,
    FOREIGN KEY (user_id, tenant_id)
        REFERENCES users (id, tenant_id) ON DELETE CASCADE
);

CREATE INDEX release_center_approval_group_members_user_idx
    ON release_center_approval_group_members (tenant_id, user_id, active);

CREATE TABLE release_center_approval_policies (
    tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    policy_id TEXT NOT NULL,
    knowledge_space_id TEXT NOT NULL DEFAULT '',
    permission TEXT NOT NULL DEFAULT '',
    minimum_risk TEXT NOT NULL DEFAULT 'low'
        CHECK (minimum_risk IN ('low','medium','high','critical')),
    required_approvals INT NOT NULL CHECK (required_approvals BETWEEN 1 AND 2),
    approver_group_id TEXT NOT NULL,
    allow_requester_approval BOOLEAN NOT NULL DEFAULT FALSE,
    priority INT NOT NULL DEFAULT 0,
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, policy_id),
    FOREIGN KEY (tenant_id, approver_group_id)
        REFERENCES release_center_approval_groups (tenant_id, group_id)
);

CREATE INDEX release_center_approval_policies_match_idx
    ON release_center_approval_policies
       (tenant_id, knowledge_space_id, permission, minimum_risk, priority DESC)
    WHERE active;

ALTER TABLE release_center_requests
    ADD COLUMN policy_id TEXT,
    ADD COLUMN approver_group_id TEXT,
    ADD COLUMN allow_requester_approval BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE release_center_requests
    ADD CONSTRAINT release_center_requests_policy_fk
    FOREIGN KEY (tenant_id, policy_id)
    REFERENCES release_center_approval_policies (tenant_id, policy_id)
    DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE release_center_requests
    ADD CONSTRAINT release_center_requests_group_fk
    FOREIGN KEY (tenant_id, approver_group_id)
    REFERENCES release_center_approval_groups (tenant_id, group_id)
    DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE release_center_requests
    ADD CONSTRAINT release_center_requests_policy_group_pair_ck
    CHECK ((policy_id IS NULL) = (approver_group_id IS NULL));
