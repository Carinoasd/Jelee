BEGIN;
-- Image references per catalog item (G40). Rows only point at user assets:
-- a library root plus a confined relative path, or a public HTTPS URL. Jelee
-- never deletes the referenced file; content columns describe the copy kept
-- in the content-addressed image store once it has been read or fetched.
CREATE TABLE item_images (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 item_id uuid NOT NULL,
 library_id uuid NOT NULL,
 image_type text NOT NULL CHECK(image_type IN ('Primary','Backdrop','Logo','ClearLogo','Banner','ClearArt','Art','Disc','Thumb','Landscape','Chapter','Box','BoxRear','Menu','Profile')),
 image_index integer NOT NULL CHECK(image_index BETWEEN 0 AND 9999),
 source_kind text NOT NULL CHECK(source_kind IN ('local','nfo','remote','embedded')),
 root_id uuid,
 relative_path text CHECK(
  octet_length(relative_path) BETWEEN 1 AND 1024 AND
  relative_path !~ '[[:cntrl:]]' AND relative_path !~ '(^|/)\.{1,2}(/|$)' AND
  relative_path NOT LIKE '/%' AND relative_path NOT LIKE '%/' AND
  position('//' in relative_path)=0 AND position(E'\\' in relative_path)=0 AND
  position(':' in relative_path)=0 AND cardinality(string_to_array(relative_path,'/'))<=128),
 remote_url text CHECK(
  octet_length(remote_url) BETWEEN 9 AND 2048 AND
  remote_url ~ '^https://[^/?#@[:space:][:cntrl:]]+([/?#][^[:space:][:cntrl:]]*)?$'),
 content_sha256 bytea CHECK(octet_length(content_sha256)=32),
 width integer CHECK(width BETWEEN 1 AND 65535),
 height integer CHECK(height BETWEEN 1 AND 65535),
 format text CHECK(format IN ('jpeg','png','webp','avif','gif','bmp','tiff')),
 byte_size bigint CHECK(byte_size>0),
 average_color integer CHECK(average_color BETWEEN 0 AND 16777215),
 fetched_at timestamptz CHECK(isfinite(fetched_at)),
 source_mtime_unix_nano bigint,
 source_size bigint CHECK(source_size>=0),
 locked boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK(isfinite(created_at)),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK(isfinite(updated_at)),
 FOREIGN KEY(item_id,library_id) REFERENCES items(id,library_id) ON DELETE CASCADE,
 -- The root must belong to the item's own library; the relative path check
 -- above keeps the reference lexically inside that root.
 FOREIGN KEY(root_id,library_id) REFERENCES library_roots(id,library_id) ON DELETE CASCADE,
 CONSTRAINT item_images_slot UNIQUE(item_id,image_type,image_index,source_kind),
 -- Only galleries carry more than one image of a type.
 CONSTRAINT item_images_index_type CHECK(image_index=0 OR image_type IN ('Backdrop','Chapter')),
 CONSTRAINT item_images_path_pair CHECK((root_id IS NULL)=(relative_path IS NULL)),
 CONSTRAINT item_images_reference CHECK(CASE source_kind
  WHEN 'local' THEN root_id IS NOT NULL AND remote_url IS NULL AND lower(relative_path) ~ '\.(jpg|jpeg|png|webp|avif|gif|bmp|tiff)$'
  WHEN 'embedded' THEN root_id IS NOT NULL AND remote_url IS NULL
  WHEN 'remote' THEN root_id IS NULL AND remote_url IS NOT NULL
  ELSE (root_id IS NULL)<>(remote_url IS NULL) AND (root_id IS NULL OR lower(relative_path) ~ '\.(jpg|jpeg|png|webp|avif|gif|bmp|tiff)$') END),
 -- File attributes describe a local reference; remote URLs have none.
 CONSTRAINT item_images_source_attributes CHECK((source_mtime_unix_nano IS NULL)=(source_size IS NULL) AND (source_size IS NULL OR root_id IS NOT NULL)),
 CONSTRAINT item_images_content CHECK(CASE WHEN content_sha256 IS NULL
  THEN width IS NULL AND height IS NULL AND format IS NULL AND byte_size IS NULL AND average_color IS NULL AND fetched_at IS NULL
  ELSE format IS NOT NULL AND byte_size IS NOT NULL AND fetched_at IS NOT NULL AND (width IS NULL)=(height IS NULL) END),
 CONSTRAINT item_images_times CHECK(updated_at>=created_at)
);
-- At most one user-locked image per slot; the lock wins over source priority.
CREATE UNIQUE INDEX item_images_locked_slot ON item_images(item_id,image_type,image_index) WHERE locked;
CREATE INDEX item_images_root_idx ON item_images(root_id,relative_path) WHERE root_id IS NOT NULL;
CREATE INDEX item_images_library_idx ON item_images(library_id,item_id);
CREATE INDEX item_images_content_idx ON item_images(content_sha256) WHERE content_sha256 IS NOT NULL;

-- Database index of the filesystem variant store. Variants are derived and
-- rebuildable; the store directory stays authoritative for the bytes.
CREATE TABLE image_variants (
 content_sha256 bytea NOT NULL CHECK(octet_length(content_sha256)=32),
 variant_key bytea NOT NULL CHECK(octet_length(variant_key)=32),
 byte_size bigint NOT NULL CHECK(byte_size>0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK(isfinite(created_at)),
 last_access timestamptz NOT NULL DEFAULT clock_timestamp() CHECK(isfinite(last_access)),
 PRIMARY KEY(content_sha256,variant_key)
);
CREATE INDEX image_variants_lru_idx ON image_variants(last_access,content_sha256,variant_key);
COMMIT;
