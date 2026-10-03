BEGIN;
LOCK TABLE media_sidecar_tracks IN ACCESS EXCLUSIVE MODE;
-- Sidecar rows are scan observations, but schema 61 has nowhere to keep the
-- track metadata and charset decisions they carry, so retained rows block the
-- downgrade exactly like retained item images do.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM media_sidecar_tracks) THEN
  RAISE EXCEPTION 'retained sidecar tracks prevent downgrade' USING ERRCODE='55000';
 END IF;
END $$;
DROP TABLE media_sidecar_tracks;
ALTER TABLE media_sources DROP CONSTRAINT media_sources_id_library_key;
COMMIT;
