BEGIN;
-- G45 developer mode. One row holds the session every instance and the CLI
-- share; it is never inherited by default (the runtime switches it off at
-- start unless dev.persistAcrossRestart is set). Toggles hold catalogue
-- names only; an inactive session holds none.
CREATE TABLE dev_mode_state (
 id boolean PRIMARY KEY DEFAULT true CHECK (id),
 active boolean NOT NULL DEFAULT false,
 enabled_at timestamptz,
 expires_at timestamptz,
 source text NOT NULL DEFAULT '' CHECK (length(source)<=64),
 toggles text[] NOT NULL DEFAULT '{}' CHECK (cardinality(toggles)<=64 AND array_position(toggles,NULL) IS NULL),
 version bigint NOT NULL DEFAULT 0 CHECK (version>=0),
 updated_at timestamptz NOT NULL DEFAULT now(),
 -- A session lasts at most 24 hours (devmode.MaxTTL).
 CHECK (NOT active OR (enabled_at IS NOT NULL AND expires_at>enabled_at AND expires_at<=enabled_at+interval '24 hours')),
 CHECK (active OR cardinality(toggles)=0)
);
INSERT INTO dev_mode_state(id) VALUES(true);

-- One-time enable tokens, issued through the loopback-only entry. Only the
-- SHA-256 digest is stored; a token lives at most 15 minutes and is redeemed
-- once.
CREATE TABLE dev_mode_tokens (
 digest bytea PRIMARY KEY CHECK (octet_length(digest)=32),
 issued_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL,
 redeemed_at timestamptz,
 CHECK (expires_at>issued_at AND expires_at<=issued_at+interval '15 minutes')
);
COMMIT;
