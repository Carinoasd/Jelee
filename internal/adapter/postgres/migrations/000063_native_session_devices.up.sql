BEGIN;
-- Native sessions from password login (G07.4, G24.2). Only an administrator
-- may allow a user to obtain one; every existing and new user starts without
-- that right, so password login keeps issuing web sessions only.
ALTER TABLE users ADD COLUMN allow_native boolean NOT NULL DEFAULT false;

-- Client identity reported by a native login and the last authenticated use
-- of a session. All are client-supplied labels or observations, never a
-- device proof (G47.1 will match on them). Absent values are NULL, not empty.
ALTER TABLE sessions
 ADD COLUMN device_id text CONSTRAINT sessions_device_id_check CHECK(octet_length(device_id) BETWEEN 1 AND 256 AND device_id !~ '[[:cntrl:]]'),
 ADD COLUMN client_name text CONSTRAINT sessions_client_name_check CHECK(octet_length(client_name) BETWEEN 1 AND 128 AND client_name !~ '[[:cntrl:]]'),
 ADD COLUMN client_version text CONSTRAINT sessions_client_version_check CHECK(octet_length(client_version) BETWEEN 1 AND 64 AND client_version !~ '[[:cntrl:]]'),
 ADD COLUMN last_seen_at timestamptz CONSTRAINT sessions_last_seen_at_check CHECK(isfinite(last_seen_at)),
 ADD COLUMN last_ip text CONSTRAINT sessions_last_ip_check CHECK(octet_length(last_ip) BETWEEN 2 AND 45 AND last_ip ~ '^[0-9A-Fa-f.:]+$');
COMMIT;
