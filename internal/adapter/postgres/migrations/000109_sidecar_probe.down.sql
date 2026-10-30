BEGIN;
LOCK TABLE media_sidecar_tracks IN ACCESS EXCLUSIVE MODE;
-- Probe results, ".sub" kinds and pairs are derived from the files and are
-- rebuilt by the next scan and probe pass, so schema 108 simply forgets
-- them; the sidecar rows themselves stay.
DROP INDEX media_sidecar_tracks_probe_pending_idx;
ALTER TABLE media_sidecar_tracks
 DROP CONSTRAINT media_sidecar_tracks_paired,
 DROP CONSTRAINT media_sidecar_tracks_source_key,
 DROP COLUMN paired_track_id,
 DROP COLUMN subtitle_kind,
 DROP COLUMN duration_micros,
 DROP COLUMN bit_rate,
 DROP COLUMN sample_rate,
 DROP COLUMN channel_layout,
 DROP COLUMN channels,
 DROP COLUMN profile,
 DROP COLUMN codec,
 DROP COLUMN probed_modified_unix_nano,
 DROP COLUMN probed_size,
 DROP COLUMN probe_state;
COMMIT;
