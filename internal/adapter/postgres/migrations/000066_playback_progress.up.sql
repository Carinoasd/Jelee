BEGIN;
-- Playback sessions (G23.1, G23.2). One row per user and play key: repeated
-- start reports of the same key rejoin the row instead of adding one. Progress
-- reports are buffered by the server and written in batches, so the row holds
-- the latest flushed state, not every report. Device and client labels are
-- copied from the authenticated session at start so the history survives the
-- session; they are client-supplied labels, never proofs. Sessions go with
-- their user and item.
CREATE TABLE playback_sessions (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 play_key text NOT NULL CONSTRAINT playback_sessions_play_key_check CHECK(octet_length(play_key) BETWEEN 1 AND 128 AND play_key ~ '^[A-Za-z0-9._:-]+$'),
 auth_session_id uuid REFERENCES sessions(id) ON DELETE SET NULL,
 device_id text CONSTRAINT playback_sessions_device_id_check CHECK(octet_length(device_id) BETWEEN 1 AND 256 AND device_id !~ '[[:cntrl:]]'),
 client_name text CONSTRAINT playback_sessions_client_name_check CHECK(octet_length(client_name) BETWEEN 1 AND 128 AND client_name !~ '[[:cntrl:]]'),
 item_id uuid NOT NULL,
 library_id uuid NOT NULL,
 source_id uuid REFERENCES media_sources(id) ON DELETE SET NULL,
 delivery text NOT NULL DEFAULT 'direct' CONSTRAINT playback_sessions_delivery_check CHECK(delivery='direct'),
 state text NOT NULL CONSTRAINT playback_sessions_state_check CHECK(state IN ('active','stopped','failed','timed_out')),
 failure_reason text CONSTRAINT playback_sessions_failure_reason_check CHECK(failure_reason IN ('transcode_disabled','codec_unsupported','client_blocked','permission_denied','playback_error')),
 started_at timestamptz NOT NULL CONSTRAINT playback_sessions_started_at_check CHECK(isfinite(started_at)),
 last_report_at timestamptz NOT NULL CONSTRAINT playback_sessions_last_report_at_check CHECK(isfinite(last_report_at)),
 ended_at timestamptz CONSTRAINT playback_sessions_ended_at_check CHECK(isfinite(ended_at)),
 position_ticks bigint NOT NULL DEFAULT 0 CONSTRAINT playback_sessions_position_check CHECK(position_ticks BETWEEN 0 AND 86400000000000),
 runtime_ticks bigint CONSTRAINT playback_sessions_runtime_check CHECK(runtime_ticks BETWEEN 1 AND 86400000000000),
 paused boolean NOT NULL DEFAULT false,
 report_count integer NOT NULL DEFAULT 0 CONSTRAINT playback_sessions_report_count_check CHECK(report_count>=0),
 sample_count integer NOT NULL DEFAULT 0 CONSTRAINT playback_sessions_sample_count_check CHECK(sample_count BETWEEN 0 AND 4096),
 FOREIGN KEY(item_id,library_id) REFERENCES items(id,library_id) ON DELETE CASCADE,
 UNIQUE(user_id,play_key),
 CONSTRAINT playback_sessions_ended_check CHECK((state='active')=(ended_at IS NULL)),
 CONSTRAINT playback_sessions_failed_check CHECK((state='failed')=(failure_reason IS NOT NULL))
);
-- Timeout sweep, retention purge and the latest session of a user and item;
-- the item, source and session indexes serve the cascades.
CREATE INDEX playback_sessions_active_idx ON playback_sessions(last_report_at) WHERE state='active';
CREATE INDEX playback_sessions_ended_idx ON playback_sessions(ended_at) WHERE ended_at IS NOT NULL;
CREATE INDEX playback_sessions_user_item_idx ON playback_sessions(user_id,item_id,last_report_at);
CREATE INDEX playback_sessions_item_idx ON playback_sessions(item_id);
CREATE INDEX playback_sessions_source_idx ON playback_sessions(source_id) WHERE source_id IS NOT NULL;
CREATE INDEX playback_sessions_auth_session_idx ON playback_sessions(auth_session_id) WHERE auth_session_id IS NOT NULL;

-- The report stream kept for watch statistics (G23.3): state changes, at
-- most one progress sample per sampling interval and both sides of every
-- discontinuity. Bounded per session.
CREATE TABLE playback_samples (
 session_id uuid NOT NULL REFERENCES playback_sessions(id) ON DELETE CASCADE,
 seq integer NOT NULL CONSTRAINT playback_samples_seq_check CHECK(seq BETWEEN 0 AND 4095),
 at timestamptz NOT NULL CONSTRAINT playback_samples_at_check CHECK(isfinite(at)),
 kind text NOT NULL CONSTRAINT playback_samples_kind_check CHECK(kind IN ('start','progress','pause','resume','seek','stop','fail')),
 position_ticks bigint NOT NULL CONSTRAINT playback_samples_position_check CHECK(position_ticks BETWEEN 0 AND 86400000000000),
 paused boolean NOT NULL DEFAULT false,
 PRIMARY KEY(session_id,seq)
);

-- Per user progress on a logical item (G20.4): one resume point and played
-- state shared by every version of the item. last_source_id remembers the
-- version last played.
CREATE TABLE user_item_data (
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 item_id uuid NOT NULL REFERENCES items(id) ON DELETE CASCADE,
 resume_ticks bigint NOT NULL DEFAULT 0 CONSTRAINT user_item_data_resume_check CHECK(resume_ticks BETWEEN 0 AND 86400000000000),
 played boolean NOT NULL DEFAULT false,
 play_count integer NOT NULL DEFAULT 0 CONSTRAINT user_item_data_play_count_check CHECK(play_count>=0),
 last_played_at timestamptz CONSTRAINT user_item_data_last_played_at_check CHECK(isfinite(last_played_at)),
 last_source_id uuid REFERENCES media_sources(id) ON DELETE SET NULL,
 updated_at timestamptz NOT NULL CONSTRAINT user_item_data_updated_at_check CHECK(isfinite(updated_at)),
 PRIMARY KEY(user_id,item_id)
);
CREATE INDEX user_item_data_resume_idx ON user_item_data(user_id,last_played_at DESC,item_id) WHERE resume_ticks>0 AND NOT played;
CREATE INDEX user_item_data_item_idx ON user_item_data(item_id);
CREATE INDEX user_item_data_source_idx ON user_item_data(last_source_id) WHERE last_source_id IS NOT NULL;
COMMIT;
