BEGIN;
-- G33.3 user interface preferences: one row per user, written only by the
-- user through PUT /api/v1/users/me/preferences. A user without a row reads
-- the defaults. Preferences change presentation only, so they are neither
-- audited nor exported with metadata backups.
CREATE TABLE user_preferences (
 user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 theme text NOT NULL DEFAULT 'system' CONSTRAINT user_preferences_theme_check CHECK(theme IN ('system','light','dark')),
 density text NOT NULL DEFAULT 'comfortable' CONSTRAINT user_preferences_density_check CHECK(density IN ('comfortable','compact')),
 updated_at timestamptz NOT NULL DEFAULT now()
);

-- Known clients report whether a block rule covers them (G47.5). The lookup
-- compares a client's device ID or user agent with enabled deny rules, so
-- listing a page of clients does not scan every rule once per client.
CREATE INDEX client_rules_block_lookup_idx ON client_rules(dimension,pattern) WHERE enabled AND action='deny';
COMMIT;
