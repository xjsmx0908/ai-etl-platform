CREATE TABLE IF NOT EXISTS agent_approvals (
    id             TEXT PRIMARY KEY,
    run_id         TEXT NOT NULL,
    tenant_id      TEXT NOT NULL,
    step_index     INTEGER NOT NULL,
    tool_name      TEXT NOT NULL,
    tool_arguments JSONB NOT NULL DEFAULT '{}'::jsonb,
    status         TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'approved', 'rejected')),
    requested_by   TEXT NOT NULL,
    requested_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_by     TEXT NOT NULL DEFAULT '',
    decided_at     TIMESTAMPTZ,
    reason         TEXT NOT NULL DEFAULT '',
    UNIQUE (tenant_id, run_id, step_index, tool_name)
);

CREATE INDEX agent_approvals_tenant_run_idx
    ON agent_approvals (tenant_id, run_id, step_index);

CREATE INDEX agent_approvals_tenant_status_idx
    ON agent_approvals (tenant_id, status, requested_at DESC);
