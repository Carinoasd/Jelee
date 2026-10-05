BEGIN;
-- G07.8 optional time-based second factor. The authenticator secret is
-- sealed with the server master key and bound to the user (AES-GCM context),
-- so storage and backups never hold it in plain text. enabled_at is NULL
-- while an enrollment waits for its confirming code. last_step is the last
-- accepted RFC 6238 time step: a code is accepted only for a later step,
-- so no code works twice.
CREATE TABLE user_totp (
 user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 secret_sealed bytea NOT NULL CONSTRAINT user_totp_secret_check CHECK(octet_length(secret_sealed) BETWEEN 40 AND 256),
 created_at timestamptz NOT NULL DEFAULT now(),
 enabled_at timestamptz,
 last_step bigint NOT NULL DEFAULT 0 CONSTRAINT user_totp_last_step_check CHECK(last_step>=0)
);

-- One-time recovery codes: SHA-256 digests bound to the user, never the
-- codes. used_at marks a spent code.
CREATE TABLE user_recovery_codes (
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 code_digest bytea NOT NULL CONSTRAINT user_recovery_codes_digest_check CHECK(octet_length(code_digest)=32),
 used_at timestamptz,
 PRIMARY KEY(user_id,code_digest)
);

-- The second step of a web login: a short-lived bearer challenge (digest
-- only) issued after the password verified. It is spent by the first
-- successful code and dies after a bounded number of wrong ones; the
-- auth_version snapshot makes a password change or reset invalidate it.
CREATE TABLE login_challenges (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 token_digest bytea NOT NULL UNIQUE CONSTRAINT login_challenges_digest_check CHECK(octet_length(token_digest)=32),
 auth_version bigint NOT NULL,
 device_name text NOT NULL CONSTRAINT login_challenges_device_check CHECK(octet_length(device_name)<=128 AND device_name !~ '[[:cntrl:]]'),
 created_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL,
 attempts integer NOT NULL DEFAULT 0 CONSTRAINT login_challenges_attempts_check CHECK(attempts>=0),
 used_at timestamptz
);
CREATE INDEX login_challenges_user_idx ON login_challenges(user_id);

-- Application passwords for clients that cannot ask for a code (native
-- devices, the compatibility layer). Created from a web session, shown once,
-- stored as a SHA-256 digest bound to the user; deleting one revokes the
-- sessions it issued.
CREATE TABLE app_passwords (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 name text NOT NULL CONSTRAINT app_passwords_name_check CHECK(octet_length(name) BETWEEN 1 AND 128 AND name !~ '[[:cntrl:]]'),
 digest bytea NOT NULL UNIQUE CONSTRAINT app_passwords_digest_check CHECK(octet_length(digest)=32),
 created_at timestamptz NOT NULL DEFAULT now(),
 last_used_at timestamptz
);
CREATE INDEX app_passwords_user_idx ON app_passwords(user_id);

ALTER TABLE sessions ADD COLUMN app_password_id uuid REFERENCES app_passwords(id) ON DELETE SET NULL;
CREATE INDEX sessions_app_password_idx ON sessions(app_password_id) WHERE app_password_id IS NOT NULL;
COMMIT;
