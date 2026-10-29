BEGIN;
-- Default version choice (G20.2): a user's version preferences, consulted
-- after the client's declared capabilities have excluded what it cannot play
-- as it is. Every member is optional; a user without a row has none. They
-- only order the original versions; nothing is converted.
CREATE TABLE user_version_preferences (
 user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 max_height integer CONSTRAINT user_version_preferences_max_height_check CHECK(max_height IN (480,576,720,1080,1440,2160,4320)),
 dynamic_range text CONSTRAINT user_version_preferences_dynamic_range_check CHECK(dynamic_range IN ('sdr','hdr')),
 prefer_smaller boolean,
 max_bitrate bigint CONSTRAINT user_version_preferences_max_bitrate_check CHECK(max_bitrate BETWEEN 1 AND 1000000000000),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp() CONSTRAINT user_version_preferences_updated_at_check CHECK(isfinite(updated_at)),
 CONSTRAINT user_version_preferences_values_check CHECK(num_nonnulls(max_height,dynamic_range,prefer_smaller,max_bitrate)>0)
);

-- Session level track choice (G20.4): the audio and subtitle a client last
-- switched to while playing one version. One row per user and item (the
-- latest play session wins); it is applied when the same user resumes the
-- item or asks for playback information again, and only to the version it
-- was recorded on. Rows expire after 30 days (reads ignore older rows and
-- every write deletes a bounded batch of them), a user keeps at most 1000,
-- and changing a stored track preference of the item or the user's own
-- defaults removes them.
CREATE TABLE session_track_selections (
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 item_id uuid NOT NULL REFERENCES items(id) ON DELETE CASCADE,
 source_id uuid NOT NULL REFERENCES media_sources(id) ON DELETE CASCADE,
 play_key text NOT NULL CONSTRAINT session_track_selections_play_key_check CHECK(play_key ~ '^[A-Za-z0-9._:-]{1,128}$'),
 audio_track text CONSTRAINT session_track_selections_audio_track_check CHECK(audio_track ~ '^(e:[0-9]{1,5}|x:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$'),
 subtitle_track text CONSTRAINT session_track_selections_subtitle_track_check CHECK(subtitle_track ~ '^(off|e:[0-9]{1,5}|x:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$'),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp() CONSTRAINT session_track_selections_updated_at_check CHECK(isfinite(updated_at)),
 PRIMARY KEY(user_id,item_id),
 CONSTRAINT session_track_selections_values_check CHECK(num_nonnulls(audio_track,subtitle_track)>0)
);
CREATE INDEX session_track_selections_user_age_idx ON session_track_selections(user_id,updated_at);
CREATE INDEX session_track_selections_age_idx ON session_track_selections(updated_at);
CREATE INDEX session_track_selections_item_idx ON session_track_selections(item_id);
CREATE INDEX session_track_selections_source_idx ON session_track_selections(source_id);
COMMIT;
