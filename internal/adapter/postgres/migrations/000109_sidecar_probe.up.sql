BEGIN;
-- On-demand probing of external tracks, ".sub" content kind and ".idx"
-- with ".sub" pairing (G15.1, G15.3, G16.1, G16.3).
--
-- Every column here is derived from the user's file and rebuilt by the next
-- scan and probe pass; none of it is user input.
LOCK TABLE media_sidecar_tracks IN ACCESS EXCLUSIVE MODE;
ALTER TABLE media_sidecar_tracks
 -- not_applicable: a text subtitle whose extension already names its
 -- format. pending: an audio file, a ".sub" or a ".sup" waiting for the
 -- sandboxed ffprobe pass. ready/failed: the probe result of the stamp in
 -- probed_size and probed_modified_unix_nano, which must equal the row's.
 ADD COLUMN probe_state text NOT NULL DEFAULT 'not_applicable' CHECK(probe_state IN ('pending','ready','failed','not_applicable')),
 ADD COLUMN probed_size bigint,
 ADD COLUMN probed_modified_unix_nano bigint,
 ADD COLUMN codec text CHECK(codec ~ '^[a-z0-9_]{1,32}$'),
 ADD COLUMN profile text CHECK(octet_length(profile) BETWEEN 1 AND 64 AND profile !~ '[[:cntrl:]]'),
 ADD COLUMN channels integer CHECK(channels BETWEEN 1 AND 256),
 ADD COLUMN channel_layout text CHECK(octet_length(channel_layout) BETWEEN 1 AND 32 AND channel_layout ~ '^[a-z0-9.()_-]+$'),
 ADD COLUMN sample_rate integer CHECK(sample_rate BETWEEN 1 AND 768000),
 ADD COLUMN bit_rate bigint CHECK(bit_rate>0),
 ADD COLUMN duration_micros bigint CHECK(duration_micros>=0),
 -- What the content of a ".sub" is: MPEG-PS pack headers (VobSub data) or
 -- "{n}{n}" lines (MicroDVD text). NULL while unread or unrecognized.
 ADD COLUMN subtitle_kind text CHECK(subtitle_kind IN ('microdvd','vobsub')),
 -- On an ".idx" row: the ".sub" data half with the same name next to it.
 ADD COLUMN paired_track_id uuid,
 ADD CONSTRAINT media_sidecar_tracks_source_key UNIQUE(id,source_id),
 ADD CONSTRAINT media_sidecar_tracks_paired FOREIGN KEY(paired_track_id,source_id)
  REFERENCES media_sidecar_tracks(id,source_id) ON DELETE SET NULL (paired_track_id),
 ADD CONSTRAINT media_sidecar_tracks_paired_shape CHECK(paired_track_id IS NULL OR format='idx' AND paired_track_id<>id),
 ADD CONSTRAINT media_sidecar_tracks_subtitle_kind_shape CHECK(subtitle_kind IS NULL OR format='sub'),
 -- Only audio, ".sub" and ".sup" are probed.
 ADD CONSTRAINT media_sidecar_tracks_probe_scope CHECK(probe_state='not_applicable' OR kind='audio' OR format IN ('sub','sup')),
 -- A result belongs to exactly the stamp it was read from: a changed file
 -- must go back to pending before it can be described again.
 ADD CONSTRAINT media_sidecar_tracks_probe_stamp CHECK(CASE WHEN probe_state IN ('ready','failed')
  THEN probed_size IS NOT DISTINCT FROM size AND probed_modified_unix_nano IS NOT DISTINCT FROM modified_unix_nano
  ELSE probed_size IS NULL AND probed_modified_unix_nano IS NULL END),
 ADD CONSTRAINT media_sidecar_tracks_probe_facts CHECK(probe_state='ready' OR
  codec IS NULL AND profile IS NULL AND channels IS NULL AND channel_layout IS NULL AND sample_rate IS NULL AND bit_rate IS NULL AND duration_micros IS NULL),
 ADD CONSTRAINT media_sidecar_tracks_audio_facts CHECK(kind='audio' OR
  profile IS NULL AND channels IS NULL AND channel_layout IS NULL AND sample_rate IS NULL);

UPDATE media_sidecar_tracks SET probe_state='pending' WHERE kind='audio' OR format IN ('sub','sup');
-- A ".sub" inspected before this schema was never content-checked: clearing
-- its fingerprint queues it for the next inspection pass, which also redoes
-- its charset (VobSub data keeps none).
UPDATE media_sidecar_tracks SET fingerprint=NULL WHERE format='sub' AND fingerprint IS NOT NULL;
-- Existing ".idx" files are paired with their ".sub" now; later changes are
-- kept up by the scan.
UPDATE media_sidecar_tracks i SET paired_track_id=(SELECT s.id FROM media_sidecar_tracks s
 WHERE s.source_id=i.source_id AND s.root_id=i.root_id AND s.format='sub' AND left(s.relative_path,-4)=left(i.relative_path,-4)
 ORDER BY s.relative_path LIMIT 1)
 WHERE i.format='idx';

-- The probe pass pages the waiting rows of one library by ID.
CREATE INDEX media_sidecar_tracks_probe_pending_idx ON media_sidecar_tracks(library_id,id) WHERE probe_state='pending';
COMMIT;
