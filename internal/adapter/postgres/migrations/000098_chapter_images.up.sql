BEGIN;
-- Chapter image extraction (G40.1). One row per item remembers the last
-- attempt for the probed state of its single media file: the stored chapter
-- pictures or a deterministic refusal. A row whose stamp and fingerprint
-- still equal the probe cache row means "done for this file"; any change of
-- the file (and so of the probe stamp) makes the item a candidate again.
-- Transient failures and changed files are never written here. The pictures
-- themselves are item_images rows (image_type 'Chapter', source_kind
-- 'embedded'), which schema 58 already allows.
CREATE TABLE item_chapter_image_attempts (
 item_id uuid PRIMARY KEY,
 library_id uuid NOT NULL,
 root_id uuid NOT NULL,
 relative_path text NOT NULL CHECK(
  octet_length(relative_path) BETWEEN 1 AND 1024 AND
  relative_path !~ '[[:cntrl:]]' AND relative_path !~ '(^|/)\.{1,2}(/|$)' AND
  relative_path NOT LIKE '/%' AND relative_path NOT LIKE '%/' AND
  position('//' in relative_path)=0 AND position(E'\\' in relative_path)=0 AND
  position(':' in relative_path)=0),
 source_size bigint NOT NULL CHECK(source_size>=0),
 source_mtime_unix_nano bigint NOT NULL,
 fingerprint bytea NOT NULL CHECK(octet_length(fingerprint)=32),
 outcome text NOT NULL CHECK(outcome IN ('stored','absent','invalid','too_large')),
 image_count integer NOT NULL CHECK(image_count BETWEEN 0 AND 64),
 attempted_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK(isfinite(attempted_at)),
 FOREIGN KEY(item_id,library_id) REFERENCES items(id,library_id) ON DELETE CASCADE,
 FOREIGN KEY(root_id,library_id) REFERENCES library_roots(id,library_id) ON DELETE CASCADE,
 CONSTRAINT item_chapter_image_attempts_count CHECK((outcome='stored')=(image_count>0))
);
CREATE INDEX item_chapter_image_attempts_library_idx ON item_chapter_image_attempts(library_id,item_id);
COMMIT;
