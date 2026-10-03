BEGIN;
-- External subtitle and audio files of a media source (G10.9, G15.2,
-- G16.2). Rows only point at user files: a library root plus a confined
-- relative path. Jelee serves those bytes as they are and never rewrites,
-- renames or deletes them; a UTF-8 copy is a separate rebuildable cache.
--
-- The composite key lets a sidecar row name its source together with the
-- source's library, so the root below must belong to that same library.
ALTER TABLE media_sources ADD CONSTRAINT media_sources_id_library_key UNIQUE(id,library_id);

CREATE TABLE media_sidecar_tracks (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 source_id uuid NOT NULL,
 library_id uuid NOT NULL,
 root_id uuid NOT NULL,
 relative_path text NOT NULL CHECK(
  octet_length(relative_path) BETWEEN 1 AND 1024 AND
  relative_path !~ '[[:cntrl:]]' AND relative_path !~ '(^|/)\.{1,2}(/|$)' AND
  relative_path NOT LIKE '/%' AND relative_path NOT LIKE '%/' AND
  position('//' in relative_path)=0 AND position(E'\\' in relative_path)=0 AND
  position(':' in relative_path)=0 AND cardinality(string_to_array(relative_path,'/'))<=128),
 kind text NOT NULL CHECK(kind IN ('subtitle','audio')),
 -- The lower-case file extension as ParseSidecarName reports it; ".sub"
 -- may be MicroDVD text or VobSub data, which probing decides later.
 format text NOT NULL,
 -- Canonical BCP 47 tags as produced by the sidecar name parser: a primary
 -- language, an optional title-case script and an optional region.
 language text CHECK(octet_length(language)<=35 AND language ~ '^[a-z]{2,3}(-[A-Z][a-z]{3})?(-([A-Z]{2}|[0-9]{3}))?$'),
 languages text[] NOT NULL DEFAULT '{}' CHECK(
  cardinality(languages)<=8 AND COALESCE(array_ndims(languages)=1 AND array_lower(languages,1)=1,true) AND array_position(languages,NULL) IS NULL AND
  (cardinality(languages)=0 OR array_to_string(languages,',') ~ '^[a-z]{2,3}(-[A-Z][a-z]{3})?(-([A-Z]{2}|[0-9]{3}))?(,[a-z]{2,3}(-[A-Z][a-z]{3})?(-([A-Z]{2}|[0-9]{3}))?)*$')),
 title text CHECK(octet_length(title) BETWEEN 1 AND 255 AND title !~ '[[:cntrl:]]'),
 forced boolean NOT NULL DEFAULT false,
 sdh boolean NOT NULL DEFAULT false,
 is_default boolean NOT NULL DEFAULT false,
 commentary boolean NOT NULL DEFAULT false,
 -- Detected character set of a text subtitle (IANA preferred name).
 charset text CHECK(charset IN ('UTF-8','UTF-16LE','UTF-16BE','GB18030','Big5','Shift_JIS','EUC-JP','EUC-KR','windows-1252')),
 size bigint NOT NULL CHECK(size>=0),
 modified_unix_nano bigint NOT NULL,
 -- Optional SHA-256 content or edge fingerprint; size and modification
 -- time stand for the content while it is absent.
 fingerprint bytea CHECK(octet_length(fingerprint)=32),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK(isfinite(created_at)),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK(isfinite(updated_at)),
 FOREIGN KEY(source_id,library_id) REFERENCES media_sources(id,library_id) ON DELETE CASCADE,
 -- The root must belong to the source's library; the relative path check
 -- above keeps the reference lexically inside that root.
 FOREIGN KEY(root_id,library_id) REFERENCES library_roots(id,library_id) ON DELETE CASCADE,
 CONSTRAINT media_sidecar_tracks_file UNIQUE(source_id,root_id,relative_path),
 CONSTRAINT media_sidecar_tracks_format CHECK(CASE kind
  WHEN 'subtitle' THEN format IN ('srt','ass','ssa','vtt','webvtt','ttml','dfxp','smi','sami','sub','idx','sup')
  ELSE format IN ('mka','aac','m4a','ac3','eac3','ec3','dts','dtshd','thd','truehd','mlp','flac','alac','opus','ogg','oga','mp3','wav') END),
 -- The format is the file's own extension, not a guess.
 CONSTRAINT media_sidecar_tracks_extension CHECK(right(lower(relative_path),octet_length(format)+1)='.'||format),
 CONSTRAINT media_sidecar_tracks_language CHECK(CASE WHEN language IS NULL THEN cardinality(languages)=0 ELSE cardinality(languages)>0 AND languages[1] IS NOT DISTINCT FROM language END),
 -- Only subtitles are text. Flags are kept as the file name states them for
 -- either kind; a commentary subtitle or a described-audio track is real.
 CONSTRAINT media_sidecar_tracks_charset CHECK(kind='subtitle' OR charset IS NULL),
 CONSTRAINT media_sidecar_tracks_times CHECK(updated_at>=created_at)
);
-- The file key leads with source_id and serves per-source reads; this one
-- serves root cascades and path lookups.
CREATE INDEX media_sidecar_tracks_root_idx ON media_sidecar_tracks(root_id,relative_path);
COMMIT;
