-- Reverse of 0031. Migrations are up-only in this project (migrate.go embeds
-- *.up.sql only); this file exists so a restore rehearsal has the inverse
-- written down rather than reconstructed under pressure.
DROP TABLE IF EXISTS user_invites;
