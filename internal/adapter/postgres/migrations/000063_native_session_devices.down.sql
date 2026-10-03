BEGIN;
-- Schema 62 has no native-login permission, so dropping it only withdraws
-- the right to obtain new native sessions (fail closed). Sessions already
-- issued keep working there exactly like CLI-provisioned native sessions and
-- remain revocable; only their reported client labels and last-use
-- observations are lost.
ALTER TABLE sessions
 DROP COLUMN last_ip,
 DROP COLUMN last_seen_at,
 DROP COLUMN client_version,
 DROP COLUMN client_name,
 DROP COLUMN device_id;
ALTER TABLE users DROP COLUMN allow_native;
COMMIT;
