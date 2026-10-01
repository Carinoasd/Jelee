BEGIN;
ALTER TABLE item_metadata_fields DROP CONSTRAINT item_metadata_fields_source_check;
ALTER TABLE item_metadata_fields ADD CONSTRAINT item_metadata_fields_source_check CHECK(source IN ('existing','manual','tmdb','nfo'));
ALTER TABLE item_metadata_fields ADD COLUMN nfo_origin jsonb;
ALTER TABLE item_metadata_fields ADD CONSTRAINT item_metadata_fields_nfo_origin_check CHECK(
 (source='nfo' AND nfo_origin IS NOT NULL AND COALESCE(
  jsonb_typeof(nfo_origin)='object'
  AND nfo_origin ?& ARRAY['sourceId','rootId','generation','sha256','identityDigest','projection','readAt','locked']
  AND nfo_origin - ARRAY['sourceId','rootId','generation','sha256','identityDigest','projection','readAt','locked'] = '{}'::jsonb
  AND jsonb_typeof(nfo_origin->'sourceId')='string' AND (nfo_origin->>'sourceId') ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
  AND jsonb_typeof(nfo_origin->'rootId')='string' AND (nfo_origin->>'rootId') ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
  AND jsonb_typeof(nfo_origin->'generation')='number' AND (nfo_origin->>'generation') ~ '^[1-9][0-9]{0,18}$' AND (nfo_origin->>'generation')::numeric <= 9223372036854775807
  AND jsonb_typeof(nfo_origin->'sha256')='string' AND (nfo_origin->>'sha256') ~ '^[0-9a-f]{64}$'
  AND jsonb_typeof(nfo_origin->'identityDigest')='string' AND (nfo_origin->>'identityDigest') ~ '^[0-9a-f]{64}$'
  AND jsonb_typeof(nfo_origin->'projection')='string' AND nfo_origin->>'projection'='four-text-fields-v1'
  AND jsonb_typeof(nfo_origin->'locked')='boolean'
  AND jsonb_typeof(nfo_origin->'readAt')='string'
  AND isfinite((nfo_origin->>'readAt')::timestamptz)
  AND (nfo_origin->>'readAt')::timestamptz >= timestamptz '0001-01-01 00:00:00+00'
  AND (nfo_origin->>'readAt')::timestamptz <= timestamptz '9999-12-31 23:59:59.999999+00'
 ,false))
 OR (source<>'nfo' AND nfo_origin IS NULL)
);
COMMIT;
