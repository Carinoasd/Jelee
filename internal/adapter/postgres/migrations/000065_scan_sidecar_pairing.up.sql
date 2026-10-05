BEGIN;
-- Scan pairing of external subtitle and audio files (G15.3, G16.2).
--
-- The owner directory of a scanned file is its parent folder, except that a
-- file directly inside a sidecar folder (Sub, Subs, Subtitle, Subtitles,
-- Audio, Audios in any ASCII case) belongs to the folder above it. Videos
-- and the sidecars that may belong to them therefore share one owner, and a
-- catalog sync batch finds them with one indexed lookup per directory
-- instead of reading every file below it. The root folder is ''. This must
-- stay identical to domain.SidecarOwnerDirectory.
CREATE FUNCTION inventory_sidecar_owner(file_path text) RETURNS text
 LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
 SELECT CASE
  WHEN file_path ~ '(^|/)([Ss][Uu][Bb]([Ss]|[Tt][Ii][Tt][Ll][Ee][Ss]?)?|[Aa][Uu][Dd][Ii][Oo][Ss]?)/[^/]+$'
  THEN regexp_replace(file_path,'(^|/)[^/]+/[^/]+$','')
  ELSE regexp_replace(file_path,'(^|/)[^/]+$','') END
$$;

-- Only videos and unclassified files take part in pairing; NFO and image
-- rows do not pay for this index.
CREATE INDEX library_inventory_sidecar_owner_idx ON library_inventory_baseline_data(library_id,snapshot_id,root_id,inventory_sidecar_owner(path))
 WHERE kind IN ('video','other');

-- Tracks still waiting for their bounded charset and edge fingerprint
-- inspection after a catalog sync wrote them.
CREATE INDEX media_sidecar_tracks_uninspected_idx ON media_sidecar_tracks(library_id,id) WHERE fingerprint IS NULL;
COMMIT;
