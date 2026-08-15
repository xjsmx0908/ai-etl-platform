-- Token version: bumped on password reset so previously-issued JWTs (which
-- carry the old version) are rejected by the auth middleware. Tokens are
-- otherwise stateless; this column is the revocation mechanism.
ALTER TABLE users ADD COLUMN token_version INT NOT NULL DEFAULT 0;
