BEGIN;
-- Schema 1 cannot represent deletion. Preserve a fail-closed account state so
-- rolling back does not reactivate a deleted account for the legacy binary.
UPDATE users SET disabled=true WHERE deleted_at IS NOT NULL;
DROP TABLE user_creation_keys;
ALTER TABLE audit_logs DROP COLUMN after_state, DROP COLUMN before_state, DROP COLUMN actor_ip, DROP COLUMN actor_id;
DROP INDEX sessions_active_user_idx;
ALTER TABLE sessions DROP COLUMN created_at, DROP COLUMN device_name;
ALTER TABLE users DROP COLUMN locked_until, DROP COLUMN failed_login, DROP COLUMN auth_version,
 DROP COLUMN deleted_at, DROP COLUMN hidden, DROP COLUMN locale, DROP COLUMN display_name, DROP COLUMN password_hash;
DROP INDEX users_name_casefold_idx;
COMMIT;
