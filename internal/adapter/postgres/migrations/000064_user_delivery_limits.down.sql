BEGIN;
-- Schema 63 has no per-user delivery limits. Dropping them returns every user
-- to the server-wide limits; nothing else depends on the overrides.
ALTER TABLE users DROP COLUMN max_kbps, DROP COLUMN max_streams;
COMMIT;
