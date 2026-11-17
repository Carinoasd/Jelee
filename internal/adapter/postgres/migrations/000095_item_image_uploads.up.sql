BEGIN;
-- G40.10/G40.11: an administrator may upload artwork for a slot. Uploaded
-- bytes live only in the content-addressed image store, so an 'upload' row
-- names neither a library file nor a URL and always carries its content.
-- Uploads rank after every automatic source; the upload API locks them.
-- Only a person uploads, so an upload row is always manual (schema 94).
ALTER TABLE item_images DROP CONSTRAINT item_images_source_kind_check;
ALTER TABLE item_images ADD CONSTRAINT item_images_source_kind_check CHECK(source_kind IN ('local','nfo','remote','embedded','upload'));
ALTER TABLE item_images DROP CONSTRAINT item_images_reference;
ALTER TABLE item_images ADD CONSTRAINT item_images_reference CHECK(CASE source_kind
 WHEN 'local' THEN root_id IS NOT NULL AND remote_url IS NULL AND lower(relative_path) ~ '\.(jpg|jpeg|png|webp|avif|gif|bmp|tiff)$'
 WHEN 'embedded' THEN root_id IS NOT NULL AND remote_url IS NULL
 WHEN 'remote' THEN root_id IS NULL AND remote_url IS NOT NULL
 WHEN 'upload' THEN root_id IS NULL AND remote_url IS NULL AND content_sha256 IS NOT NULL
 ELSE (root_id IS NULL)<>(remote_url IS NULL) AND (root_id IS NULL OR lower(relative_path) ~ '\.(jpg|jpeg|png|webp|avif|gif|bmp|tiff)$') END);
ALTER TABLE item_images ADD CONSTRAINT item_images_upload_manual CHECK(source_kind<>'upload' OR manual);
COMMIT;
