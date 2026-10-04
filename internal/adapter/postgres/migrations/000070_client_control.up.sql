BEGIN;
-- Client control (G47): administrator rules that inspect who is calling
-- (user agent, reported client and device, address, header traits) and
-- deny, restrict or merely record the request. internal/access compiles and
-- evaluates them; docs/client-control.md describes the model and its limits.

-- Server-wide policy, one row. version increases with every change that
-- alters an evaluation (a rule, this policy or a client's trust), so each
-- server instance notices the change on its next authenticated request and
-- recompiles its cached rule set.
CREATE TABLE client_control_policy (
 id boolean PRIMARY KEY DEFAULT true CONSTRAINT client_control_policy_single_row CHECK(id),
 unknown_clients text NOT NULL DEFAULT 'allow' CONSTRAINT client_control_policy_unknown_check CHECK(unknown_clients IN ('allow','read_only','deny','pending_approval')),
 exempt_admins boolean NOT NULL DEFAULT true,
 exempt_loopback boolean NOT NULL DEFAULT true,
 version bigint NOT NULL DEFAULT 1 CONSTRAINT client_control_policy_version_check CHECK(version>0),
 updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO client_control_policy DEFAULT VALUES;

-- Rules. The columns mirror access.Rule; the application validates a rule
-- set by compiling it before it commits, and these checks are the storage
-- backstop. restrict_libraries and the group and library scopes are not
-- enforced at the request gate yet, so storage refuses them rather than
-- keep a rule that would silently do nothing.
CREATE TABLE client_rules (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 dimension text NOT NULL CONSTRAINT client_rules_dimension_check CHECK(dimension IN ('user_agent','app_name','app_version','device_id','device_name','device_type','ip','api_key_fingerprint','header')),
 header_name text CONSTRAINT client_rules_header_check CHECK((dimension='header')=(header_name IS NOT NULL) AND (header_name IS NULL OR octet_length(header_name)<=256 AND header_name ~ '^[!#$%&''*+.^_`|~0-9A-Za-z-]+$')),
 match_kind text NOT NULL CONSTRAINT client_rules_match_check CHECK(match_kind IN ('exact','prefix','glob','regex','cidr','absent')),
 pattern text NOT NULL CONSTRAINT client_rules_pattern_check CHECK(octet_length(pattern)<=1024 AND (match_kind='absent')=(pattern='')),
 case_fold boolean NOT NULL DEFAULT false,
 priority integer NOT NULL DEFAULT 0 CONSTRAINT client_rules_priority_check CHECK(priority BETWEEN -1000000 AND 1000000),
 action text NOT NULL CONSTRAINT client_rules_action_check CHECK(action IN ('allow','deny','read_only','rate_limit','force_relogin','observe','shadow')),
 intent text CONSTRAINT client_rules_intent_check CHECK((action IN ('observe','shadow'))=(intent IS NOT NULL) AND (intent IS NULL OR intent IN ('allow','deny','read_only','rate_limit','force_relogin'))),
 rate_requests integer CONSTRAINT client_rules_rate_requests_check CHECK(rate_requests BETWEEN 1 AND 1000000),
 rate_period_seconds integer CONSTRAINT client_rules_rate_period_check CHECK(rate_period_seconds BETWEEN 1 AND 86400),
 scope_kind text NOT NULL DEFAULT 'global' CONSTRAINT client_rules_scope_check CHECK(scope_kind IN ('global','user','client_kind')),
 scope_values text[] NOT NULL DEFAULT '{}' CONSTRAINT client_rules_scope_values_check CHECK(cardinality(scope_values)<=1000 AND (scope_kind='global')=(cardinality(scope_values)=0)),
 window_from timestamptz,
 window_until timestamptz,
 daily_start text CONSTRAINT client_rules_daily_start_check CHECK(daily_start ~ '^[0-2][0-9]:[0-5][0-9]$'),
 daily_end text CONSTRAINT client_rules_daily_end_check CHECK(daily_end ~ '^[0-2][0-9]:[0-5][0-9]$'),
 weekdays smallint[] NOT NULL DEFAULT '{}' CONSTRAINT client_rules_weekdays_check CHECK(cardinality(weekdays)<=7 AND 0<=ALL(weekdays) AND 6>=ALL(weekdays)),
 time_zone text NOT NULL DEFAULT '' CONSTRAINT client_rules_time_zone_check CHECK(octet_length(time_zone)<=64),
 enabled boolean NOT NULL DEFAULT true,
 note text NOT NULL DEFAULT '' CONSTRAINT client_rules_note_check CHECK(octet_length(note)<=2048),
 hit_count bigint NOT NULL DEFAULT 0 CONSTRAINT client_rules_hit_count_check CHECK(hit_count>=0),
 last_hit_at timestamptz,
 created_by uuid REFERENCES users(id) ON DELETE SET NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 CONSTRAINT client_rules_rate_check CHECK((rate_requests IS NULL)=(rate_period_seconds IS NULL) AND (rate_requests IS NOT NULL)=(COALESCE(intent,action)='rate_limit')),
 CONSTRAINT client_rules_window_check CHECK(window_from IS NULL OR window_until IS NULL OR window_until>window_from),
 CONSTRAINT client_rules_daily_check CHECK((daily_start IS NULL)=(daily_end IS NULL))
);
CREATE INDEX client_rules_order_idx ON client_rules(priority DESC,id);

-- Clients seen on authenticated requests (G47.5). client_key is a digest of
-- the reported identity (application and device ID, or the user agent when
-- no device ID is reported), never a secret. trusted marks a client the
-- administrator approved: only trusted clients escape the unknown-client
-- policy. All labels are client-supplied and only describe the client.
CREATE TABLE known_clients (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 client_key text NOT NULL UNIQUE CONSTRAINT known_clients_key_check CHECK(client_key ~ '^[0-9a-f]{64}$'),
 app_name text CONSTRAINT known_clients_app_name_check CHECK(octet_length(app_name) BETWEEN 1 AND 256),
 app_version text CONSTRAINT known_clients_app_version_check CHECK(octet_length(app_version) BETWEEN 1 AND 128),
 user_agent text CONSTRAINT known_clients_user_agent_check CHECK(octet_length(user_agent) BETWEEN 1 AND 512),
 device_id text CONSTRAINT known_clients_device_id_check CHECK(octet_length(device_id) BETWEEN 1 AND 512),
 device_name text CONSTRAINT known_clients_device_name_check CHECK(octet_length(device_name) BETWEEN 1 AND 256),
 client_kind text CONSTRAINT known_clients_client_kind_check CHECK(client_kind IN ('web','native')),
 alias text CONSTRAINT known_clients_alias_check CHECK(octet_length(alias) BETWEEN 1 AND 128 AND alias !~ '[[:cntrl:]]'),
 trusted boolean NOT NULL DEFAULT false,
 first_seen_at timestamptz NOT NULL DEFAULT now(),
 last_seen_at timestamptz NOT NULL DEFAULT now(),
 last_ip inet,
 last_user_id uuid REFERENCES users(id) ON DELETE SET NULL
);
CREATE INDEX known_clients_last_seen_idx ON known_clients(last_seen_at DESC,id);
CREATE INDEX known_clients_trusted_idx ON known_clients(client_key) WHERE trusted;

-- Sessions a known client used, so kicking the client revokes them.
CREATE TABLE known_client_sessions (
 client_id uuid NOT NULL REFERENCES known_clients(id) ON DELETE CASCADE,
 session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 PRIMARY KEY(client_id,session_id)
);
CREATE INDEX known_client_sessions_session_idx ON known_client_sessions(session_id);

-- Rule hits (G47.8), aggregated per minute, rule, mode and client so a
-- rule matching heavy traffic writes a bounded number of rows. rule_id is
-- NULL for the unknown-client policy (mode 'default') and after the rule
-- was deleted. Only non-allow outcomes are stored; allow rules keep a hit
-- count. user_agent is truncated; no path, query or credential is stored.
CREATE TABLE client_control_hits (
 id bigserial PRIMARY KEY,
 bucket timestamptz NOT NULL,
 rule_id uuid REFERENCES client_rules(id) ON DELETE SET NULL,
 mode text NOT NULL CONSTRAINT client_control_hits_mode_check CHECK(mode IN ('enforced','exempt','observe','shadow','default')),
 action text NOT NULL CONSTRAINT client_control_hits_action_check CHECK(action IN ('allow','deny','read_only','rate_limit','force_relogin','pending_approval')),
 surface text NOT NULL CONSTRAINT client_control_hits_surface_check CHECK(surface IN ('native','compat')),
 user_id uuid,
 ip inet,
 user_agent text NOT NULL DEFAULT '' CONSTRAINT client_control_hits_user_agent_check CHECK(octet_length(user_agent)<=512),
 app_name text NOT NULL DEFAULT '' CONSTRAINT client_control_hits_app_name_check CHECK(octet_length(app_name)<=256),
 hits bigint NOT NULL CONSTRAINT client_control_hits_hits_check CHECK(hits>0)
);
CREATE INDEX client_control_hits_bucket_idx ON client_control_hits(bucket DESC,id DESC);
CREATE INDEX client_control_hits_rule_idx ON client_control_hits(rule_id,bucket DESC) WHERE rule_id IS NOT NULL;
COMMIT;
