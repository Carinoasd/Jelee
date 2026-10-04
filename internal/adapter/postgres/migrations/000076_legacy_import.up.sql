BEGIN;
-- Legacy database import (G04.6). A run imports one source file (identified
-- by its SHA-256) in checkpointed batches; at most one run is unfinished at a
-- time so an interrupted import resumes where its last batch committed.
CREATE TABLE legacy_import_runs (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 source_sha256 bytea NOT NULL CONSTRAINT legacy_import_runs_sha_check CHECK(octet_length(source_sha256)=32),
 source_size bigint NOT NULL CONSTRAINT legacy_import_runs_size_check CHECK(source_size>=0),
 options_digest bytea NOT NULL CONSTRAINT legacy_import_runs_options_check CHECK(octet_length(options_digest)=32),
 state text NOT NULL CONSTRAINT legacy_import_runs_state_check CHECK(state IN ('running','completed','abandoned')),
 source_counts jsonb NOT NULL CONSTRAINT legacy_import_runs_counts_check CHECK(jsonb_typeof(source_counts)='object'),
 report jsonb CONSTRAINT legacy_import_runs_report_check CHECK(report IS NULL OR jsonb_typeof(report)='object'),
 last_error text CONSTRAINT legacy_import_runs_error_check CHECK(octet_length(last_error) BETWEEN 1 AND 128),
 started_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 finished_at timestamptz,
 CONSTRAINT legacy_import_runs_finished_check CHECK((state='running')=(finished_at IS NULL))
);
CREATE UNIQUE INDEX legacy_import_runs_running_idx ON legacy_import_runs((true)) WHERE state='running';
CREATE INDEX legacy_import_runs_started_idx ON legacy_import_runs(started_at DESC);

-- One row per run and phase: the cursor after the last committed batch,
-- the outcome counts so far and the chained digest of the rows read.
CREATE TABLE legacy_import_checkpoints (
 run_id uuid NOT NULL REFERENCES legacy_import_runs(id) ON DELETE CASCADE,
 phase text NOT NULL CONSTRAINT legacy_import_checkpoints_phase_check CHECK(phase IN ('users','libraries','access','items','user_data')),
 cursor text NOT NULL DEFAULT '' CONSTRAINT legacy_import_checkpoints_cursor_check CHECK(octet_length(cursor)<=1024),
 batches integer NOT NULL DEFAULT 0 CONSTRAINT legacy_import_checkpoints_batches_check CHECK(batches>=0),
 done boolean NOT NULL DEFAULT false,
 counters jsonb NOT NULL DEFAULT '{}'::jsonb CONSTRAINT legacy_import_checkpoints_counters_check CHECK(jsonb_typeof(counters)='object'),
 digest bytea CONSTRAINT legacy_import_checkpoints_digest_check CHECK(octet_length(digest)=32),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(run_id,phase)
);

-- The ledger: which Jelee row a source row became. Re-imports consult it so
-- nothing is created twice; created tells rows this import made from rows it
-- found (merged users, matched libraries and items), origin_run the run that
-- first recorded it and run_id the last run that saw it. Targets are not
-- foreign keys: a row deleted in Jelee later is noticed and not resurrected.
CREATE TABLE legacy_import_map (
 kind text NOT NULL CONSTRAINT legacy_import_map_kind_check CHECK(kind IN ('user','library','item','user_data')),
 source_key text NOT NULL CONSTRAINT legacy_import_map_key_check CHECK(octet_length(source_key) BETWEEN 1 AND 128),
 target_id uuid NOT NULL,
 target_user uuid,
 created boolean NOT NULL,
 origin_run uuid REFERENCES legacy_import_runs(id) ON DELETE SET NULL,
 run_id uuid REFERENCES legacy_import_runs(id) ON DELETE SET NULL,
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(kind,source_key),
 CONSTRAINT legacy_import_map_user_check CHECK((kind='user_data')=(target_user IS NOT NULL))
);
CREATE INDEX legacy_import_map_target_idx ON legacy_import_map(kind,target_id);
CREATE INDEX legacy_import_map_run_idx ON legacy_import_map(run_id,kind) WHERE run_id IS NOT NULL;
CREATE INDEX legacy_import_map_origin_idx ON legacy_import_map(origin_run) WHERE origin_run IS NOT NULL;

-- Source items that could not be matched to the catalog yet, typically
-- because the library was not scanned. A later run retries them.
CREATE TABLE legacy_import_pending (
 kind text NOT NULL CONSTRAINT legacy_import_pending_kind_check CHECK(kind='item'),
 source_key text NOT NULL CONSTRAINT legacy_import_pending_key_check CHECK(octet_length(source_key) BETWEEN 1 AND 128),
 reason text NOT NULL CONSTRAINT legacy_import_pending_reason_check CHECK(reason IN ('outside_roots','not_scanned','source_file_missing')),
 path text NOT NULL CONSTRAINT legacy_import_pending_path_check CHECK(octet_length(path) BETWEEN 1 AND 4096),
 run_id uuid REFERENCES legacy_import_runs(id) ON DELETE SET NULL,
 first_seen_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(kind,source_key)
);
CREATE INDEX legacy_import_pending_run_idx ON legacy_import_pending(run_id) WHERE run_id IS NOT NULL;
COMMIT;
