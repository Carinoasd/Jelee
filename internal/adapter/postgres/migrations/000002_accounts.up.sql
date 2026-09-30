BEGIN;
-- A collision aborts this migration without renaming any existing account.
CREATE UNIQUE INDEX users_name_casefold_idx ON users (lower(name));
ALTER TABLE users
 ADD COLUMN password_hash text CHECK (password_hash IS NULL OR (length(password_hash) BETWEEN 30 AND 1024 AND password_hash LIKE '$argon2id$%')),
 ADD COLUMN display_name text NOT NULL DEFAULT '' CHECK (length(display_name) <= 128),
 ADD COLUMN locale text NOT NULL DEFAULT 'en-US' CHECK (locale IN ('en-US','zh-CN','zh-TW','ja-JP')),
 ADD COLUMN hidden boolean NOT NULL DEFAULT false,
 ADD COLUMN deleted_at timestamptz,
 ADD COLUMN auth_version bigint NOT NULL DEFAULT 1 CHECK (auth_version > 0),
 ADD COLUMN failed_login integer NOT NULL DEFAULT 0 CHECK (failed_login >= 0),
 ADD COLUMN locked_until timestamptz;
ALTER TABLE sessions
 ADD COLUMN device_name text NOT NULL DEFAULT '' CHECK (length(device_name) <= 128),
 ADD COLUMN created_at timestamptz NOT NULL DEFAULT now();
CREATE INDEX sessions_active_user_idx ON sessions(user_id,created_at,id) WHERE revoked_at IS NULL;
ALTER TABLE audit_logs
 ADD COLUMN actor_id uuid,
 ADD COLUMN actor_ip inet,
 ADD COLUMN before_state jsonb NOT NULL DEFAULT '{}'::jsonb,
 ADD COLUMN after_state jsonb NOT NULL DEFAULT '{}'::jsonb;
CREATE TABLE user_creation_keys (
 actor_id uuid NOT NULL REFERENCES users(id),
 key text NOT NULL CHECK (length(key) BETWEEN 1 AND 128),
 user_id uuid NOT NULL REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(actor_id,key)
);
COMMIT;
