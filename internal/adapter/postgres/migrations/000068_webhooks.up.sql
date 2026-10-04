BEGIN;
-- Webhook endpoints (G12.2). The signing secret, the previous secret kept
-- during a rotation and the custom header values are sealed with the
-- environment master key (AES-GCM) before they reach this table; only header
-- names are stored in plain text. URLs are HTTPS only (G12.5).
CREATE TABLE webhooks (
 id uuid PRIMARY KEY,
 name text NOT NULL CONSTRAINT webhooks_name_check CHECK(octet_length(name) BETWEEN 1 AND 128 AND name !~ '[[:cntrl:]]'),
 url text NOT NULL CONSTRAINT webhooks_url_check CHECK(octet_length(url) BETWEEN 9 AND 2048 AND url LIKE 'https://%' AND url !~ '[[:cntrl:][:space:]]'),
 enabled boolean NOT NULL DEFAULT true,
 events text[] NOT NULL DEFAULT '{}' CONSTRAINT webhooks_events_check CHECK(cardinality(events)<=64 AND array_to_string(events,',','') ~ '^([a-z]+\.[a-z_]+(,|$))*$'),
 header_names text[] NOT NULL DEFAULT '{}' CONSTRAINT webhooks_header_names_check CHECK(cardinality(header_names)<=16),
 headers_sealed bytea CONSTRAINT webhooks_headers_sealed_check CHECK(octet_length(headers_sealed) BETWEEN 33 AND 65536),
 timeout_ms integer NOT NULL CONSTRAINT webhooks_timeout_check CHECK(timeout_ms BETWEEN 1000 AND 30000),
 max_attempts integer NOT NULL CONSTRAINT webhooks_max_attempts_check CHECK(max_attempts BETWEEN 1 AND 20),
 base_delay_ms bigint NOT NULL CONSTRAINT webhooks_base_delay_check CHECK(base_delay_ms BETWEEN 1000 AND 86400000),
 max_delay_ms bigint NOT NULL CONSTRAINT webhooks_max_delay_check CHECK(max_delay_ms BETWEEN base_delay_ms AND 86400000),
 jitter double precision NOT NULL CONSTRAINT webhooks_jitter_check CHECK(jitter BETWEEN 0 AND 1),
 secret_sealed bytea NOT NULL CONSTRAINT webhooks_secret_check CHECK(octet_length(secret_sealed) BETWEEN 33 AND 1024),
 previous_secret_sealed bytea CONSTRAINT webhooks_previous_secret_check CHECK(octet_length(previous_secret_sealed) BETWEEN 33 AND 1024),
 previous_until timestamptz CONSTRAINT webhooks_previous_until_check CHECK(isfinite(previous_until)),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CONSTRAINT webhooks_headers_pair CHECK((cardinality(header_names)=0)=(headers_sealed IS NULL)),
 CONSTRAINT webhooks_previous_pair CHECK((previous_secret_sealed IS NULL)=(previous_until IS NULL))
);

-- Outbox (G12.3). Producers insert in the transaction of the change that
-- raised the event, and only while an enabled endpoint subscribes to its
-- type, so an event exists if and only if its change committed. The
-- deliverer fans each row out into webhook_deliveries once (planned_at).
-- event_id is the stable identifier consumers deduplicate on (G12.6).
CREATE TABLE webhook_outbox (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 event_id text NOT NULL DEFAULT replace(gen_random_uuid()::text,'-','') CONSTRAINT webhook_outbox_event_id_check CHECK(event_id ~ '^[A-Za-z0-9_-]{1,64}$'),
 event_type text NOT NULL CONSTRAINT webhook_outbox_event_type_check CHECK(octet_length(event_type)<=64 AND event_type ~ '^[a-z]+\.[a-z_]+$'),
 version integer NOT NULL DEFAULT 1 CONSTRAINT webhook_outbox_version_check CHECK(version>=1),
 occurred_at timestamptz NOT NULL CONSTRAINT webhook_outbox_occurred_at_check CHECK(isfinite(occurred_at)),
 subject_kind text NOT NULL CONSTRAINT webhook_outbox_subject_kind_check CHECK(subject_kind IN ('item','library','user','session','job','system')),
 subject_id text CONSTRAINT webhook_outbox_subject_id_check CHECK(octet_length(subject_id) BETWEEN 1 AND 128),
 data jsonb CONSTRAINT webhook_outbox_data_check CHECK(jsonb_typeof(data)='object' AND octet_length(data::text)<=65536),
 planned_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CONSTRAINT webhook_outbox_event_id_key UNIQUE(event_id)
);
CREATE INDEX webhook_outbox_unplanned_idx ON webhook_outbox(id) WHERE planned_at IS NULL;
CREATE INDEX webhook_outbox_created_idx ON webhook_outbox(created_at);

-- One row per (event, endpoint). A pending row is due at next_attempt_at
-- and may be leased by one deliverer at a time; lease_token fences the
-- recording of its attempt. round counts manual replays, which restart the
-- attempt budget with the same event.
CREATE TABLE webhook_deliveries (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 seq bigint GENERATED ALWAYS AS IDENTITY CONSTRAINT webhook_deliveries_seq_key UNIQUE,
 outbox_id bigint NOT NULL REFERENCES webhook_outbox(id) ON DELETE CASCADE,
 webhook_id uuid NOT NULL REFERENCES webhooks(id) ON DELETE CASCADE,
 state text NOT NULL DEFAULT 'pending' CONSTRAINT webhook_deliveries_state_check CHECK(state IN ('pending','delivered','dead')),
 attempts integer NOT NULL DEFAULT 0 CONSTRAINT webhook_deliveries_attempts_check CHECK(attempts BETWEEN 0 AND 20),
 round integer NOT NULL DEFAULT 1 CONSTRAINT webhook_deliveries_round_check CHECK(round>=1),
 next_attempt_at timestamptz CONSTRAINT webhook_deliveries_next_check CHECK(isfinite(next_attempt_at)),
 lease_until timestamptz,
 lease_token uuid,
 last_outcome text CONSTRAINT webhook_deliveries_outcome_check CHECK(last_outcome IN ('delivered','http','timeout','network','blocked','tls','invalid')),
 last_status integer CONSTRAINT webhook_deliveries_status_check CHECK(last_status BETWEEN 100 AND 599),
 last_attempt_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CONSTRAINT webhook_deliveries_pair UNIQUE(outbox_id,webhook_id),
 CONSTRAINT webhook_deliveries_due CHECK((state='pending')=(next_attempt_at IS NOT NULL)),
 CONSTRAINT webhook_deliveries_lease CHECK((lease_until IS NULL)=(lease_token IS NULL) AND (state='pending' OR lease_token IS NULL))
);
CREATE INDEX webhook_deliveries_due_idx ON webhook_deliveries(next_attempt_at) WHERE state='pending';
CREATE INDEX webhook_deliveries_log_idx ON webhook_deliveries(webhook_id,seq DESC);

-- The queryable attempt log (G12.3).
CREATE TABLE webhook_delivery_attempts (
 delivery_id uuid NOT NULL REFERENCES webhook_deliveries(id) ON DELETE CASCADE,
 round integer NOT NULL CONSTRAINT webhook_delivery_attempts_round_check CHECK(round>=1),
 attempt integer NOT NULL CONSTRAINT webhook_delivery_attempts_attempt_check CHECK(attempt BETWEEN 1 AND 20),
 started_at timestamptz NOT NULL,
 finished_at timestamptz NOT NULL,
 outcome text NOT NULL CONSTRAINT webhook_delivery_attempts_outcome_check CHECK(outcome IN ('delivered','http','timeout','network','blocked','tls','invalid')),
 status_code integer CONSTRAINT webhook_delivery_attempts_status_check CHECK(status_code BETWEEN 100 AND 599),
 next_attempt_at timestamptz,
 PRIMARY KEY(delivery_id,round,attempt)
);
COMMIT;
