-- Invitations are the only way a person obtains an account without an
-- administrator creating one for them. The row is the whole state machine: an
-- invite is pending when consumed_at, revoked_at are both NULL and expires_at is
-- still in the future, and it has exactly one terminal state after that.
--
-- The token itself is never stored. Like platform_sessions.credential_digest,
-- this table keeps only the SHA-256 of the credential, so reading the database
-- does not hand anyone a working invite link.
CREATE TABLE user_invites (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id TEXT NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    username TEXT NOT NULL CHECK (
        username <> '' AND
        username = btrim(username) AND
        char_length(username) <= 128
    ),
    display_name TEXT NOT NULL DEFAULT '' CHECK (display_name = btrim(display_name)),
    email TEXT NOT NULL DEFAULT '' CHECK (email = btrim(email)),
    role TEXT NOT NULL CHECK (role IN ('admin', 'user', 'readonly')),
    token_digest BYTEA NOT NULL UNIQUE CHECK (octet_length(token_digest) = 32),
    created_by TEXT NOT NULL CHECK (
        created_by <> '' AND created_by = btrim(created_by) AND char_length(created_by) <= 256
    ),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    consumed_user_id UUID REFERENCES users (id) ON DELETE SET NULL,
    revoked_at TIMESTAMPTZ,
    revoked_by TEXT NOT NULL DEFAULT '' CHECK (
        (revoked_by = '' OR (revoked_by = btrim(revoked_by) AND char_length(revoked_by) <= 256))
    ),
    CHECK (expires_at > created_at),
    -- An invite ends exactly once, and each terminal state carries the evidence
    -- that produced it. Without this, "consumed" and "revoked" could both be set
    -- and the audit trail would say two different things about one link.
    CHECK (consumed_at IS NULL OR revoked_at IS NULL),
    -- A user id may only appear once the invite was consumed. It may be NULL
    -- again afterwards: the account can be deleted, and ON DELETE SET NULL keeps
    -- the invite row (the audit record of "this link was used") rather than
    -- cascading the deletion into it.
    CHECK (consumed_at IS NOT NULL OR consumed_user_id IS NULL),
    CHECK ((revoked_at IS NULL) = (revoked_by = ''))
);

-- The administrator list is always "this tenant, newest first".
CREATE INDEX user_invites_tenant_created_idx
    ON user_invites (tenant_id, created_at DESC);

-- At most one live invite per username per tenant, so two working links cannot
-- race to create the same account. Terminal rows are excluded, which is what
-- lets an administrator invite a username again after the previous link was
-- consumed, revoked, or superseded.
--
-- expires_at cannot appear in the predicate: it is compared against the clock,
-- and a partial index predicate has to be immutable. Expiry is therefore
-- enforced where it matters (the claim in ConsumeInvite) and reported by the
-- store's State helper, not by this index.
CREATE UNIQUE INDEX user_invites_live_username_idx
    ON user_invites (tenant_id, lower(username))
    WHERE consumed_at IS NULL AND revoked_at IS NULL;
