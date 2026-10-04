BEGIN;
-- Network restrictions on libraries (G48.5), the request-level library
-- restriction of client control (G47 restrict_libraries) and share links
-- with guest sessions (G48.6). The unified filter in visibility.go is the
-- only reader of library_network_rules and share_links scopes;
-- docs/access-control.md describes the precedence.

-- A library with at least one enabled rule is visible only to requests
-- that at least one of its enabled rules matches. A rule matches when every
-- condition it sets holds for the request: network (lan: the trusted-proxy
-- resolved client address is private, unique local or loopback; wan: it is
-- not; any: no condition), cidrs (the address lies in one of them) and
-- client_kinds (the server-issued session kind). Administrators are only
-- subject to rules with include_admins.
CREATE TABLE library_network_rules (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 library_id uuid NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
 network text NOT NULL DEFAULT 'any' CONSTRAINT library_network_rules_network_check CHECK(network IN ('any','lan','wan')),
 cidrs cidr[] NOT NULL DEFAULT '{}' CONSTRAINT library_network_rules_cidrs_check CHECK(cardinality(cidrs)<=64 AND array_position(cidrs,NULL) IS NULL),
 client_kinds text[] NOT NULL DEFAULT '{}' CONSTRAINT library_network_rules_kinds_check CHECK(client_kinds <@ ARRAY['web','native']::text[] AND cardinality(client_kinds)<=2),
 include_admins boolean NOT NULL DEFAULT false,
 enabled boolean NOT NULL DEFAULT true,
 note text NOT NULL DEFAULT '' CONSTRAINT library_network_rules_note_check CHECK(octet_length(note)<=2048),
 created_by uuid REFERENCES users(id) ON DELETE SET NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX library_network_rules_library_idx ON library_network_rules(library_id);

-- Share links. The token is shown once; only its SHA-256 is stored. A
-- share covers a whole library (item_id NULL) or an item and its
-- descendants, which must belong to library_id. Each share has exactly one
-- guest account (users.share_id) whose sessions the link issues; revoking
-- the share revokes those sessions in the same transaction, and the filter
-- also checks revoked_at and expires_at itself.
CREATE TABLE share_links (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 token_hash bytea NOT NULL UNIQUE CONSTRAINT share_links_token_hash_check CHECK(octet_length(token_hash)=32),
 library_id uuid NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
 item_id uuid,
 expires_at timestamptz NOT NULL,
 read_only boolean NOT NULL DEFAULT true,
 allow_playback boolean NOT NULL DEFAULT false,
 max_streams smallint NOT NULL DEFAULT 1 CONSTRAINT share_links_max_streams_check CHECK(max_streams BETWEEN 1 AND 16),
 note text NOT NULL DEFAULT '' CONSTRAINT share_links_note_check CHECK(octet_length(note)<=512),
 created_by uuid REFERENCES users(id) ON DELETE SET NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 revoked_at timestamptz,
 revoked_by uuid REFERENCES users(id) ON DELETE SET NULL,
 CONSTRAINT share_links_item_fkey FOREIGN KEY(item_id,library_id) REFERENCES items(id,library_id) ON DELETE CASCADE,
 CONSTRAINT share_links_expiry_check CHECK(expires_at>created_at)
);
CREATE INDEX share_links_created_idx ON share_links(created_at DESC,id DESC);
CREATE INDEX share_links_item_idx ON share_links(item_id) WHERE item_id IS NOT NULL;
CREATE INDEX share_links_library_idx ON share_links(library_id);

ALTER TABLE users
 ADD COLUMN share_id uuid CONSTRAINT users_share_id_key UNIQUE REFERENCES share_links(id) ON DELETE CASCADE,
 ADD CONSTRAINT users_share_guest_check CHECK(share_id IS NULL OR NOT is_admin);

-- restrict_libraries narrows the libraries one request may see; the gate
-- passes the set to the unified filter as a statement parameter.
ALTER TABLE client_rules
 DROP CONSTRAINT client_rules_action_check,
 ADD CONSTRAINT client_rules_action_check CHECK(action IN ('allow','deny','read_only','rate_limit','force_relogin','restrict_libraries','observe','shadow')),
 DROP CONSTRAINT client_rules_intent_check,
 ADD CONSTRAINT client_rules_intent_check CHECK((action IN ('observe','shadow'))=(intent IS NOT NULL) AND (intent IS NULL OR intent IN ('allow','deny','read_only','rate_limit','force_relogin','restrict_libraries'))),
 ADD COLUMN libraries uuid[] NOT NULL DEFAULT '{}',
 ADD CONSTRAINT client_rules_libraries_check CHECK(cardinality(libraries)<=1000 AND array_position(libraries,NULL) IS NULL AND (cardinality(libraries)>0)=(COALESCE(intent,action)='restrict_libraries'));
ALTER TABLE client_control_hits
 DROP CONSTRAINT client_control_hits_action_check,
 ADD CONSTRAINT client_control_hits_action_check CHECK(action IN ('allow','deny','read_only','rate_limit','force_relogin','restrict_libraries','pending_approval'));
COMMIT;
