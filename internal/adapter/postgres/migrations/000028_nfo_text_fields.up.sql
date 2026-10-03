BEGIN;
ALTER TABLE item_metadata_fields DROP CONSTRAINT item_metadata_fields_field_check;
ALTER TABLE item_metadata_fields ADD CONSTRAINT item_metadata_fields_field_check CHECK(field IN ('title','originalTitle','overview','date','sortTitle','tagline','outline','mpaa','certification'));
ALTER TABLE item_metadata_fields DROP CONSTRAINT item_metadata_fields_check;
ALTER TABLE item_metadata_fields ADD CONSTRAINT item_metadata_fields_check CHECK(CASE field WHEN 'title' THEN CASE source WHEN 'existing' THEN length(value) BETWEEN 1 AND 1024 ELSE octet_length(value) BETWEEN 1 AND 1024 AND length(btrim(value))>0 END WHEN 'originalTitle' THEN octet_length(value)<=1024 WHEN 'sortTitle' THEN octet_length(value)<=1024 WHEN 'tagline' THEN octet_length(value)<=1024 WHEN 'outline' THEN octet_length(value)<=16384 WHEN 'mpaa' THEN octet_length(value)<=1024 WHEN 'certification' THEN octet_length(value)<=1024 WHEN 'overview' THEN octet_length(value)<=16384 WHEN 'date' THEN value='' OR (value ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' AND to_char(to_date(value,'YYYY-MM-DD'),'YYYY-MM-DD')=value AND left(value,4)<>'0000') ELSE false END);
ALTER TABLE item_metadata_fields DROP CONSTRAINT item_metadata_fields_nfo_origin_check;
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
  AND jsonb_typeof(nfo_origin->'projection')='string' AND nfo_origin->>'projection' IN ('four-text-fields-v1','five-field-projection-v1','extended-text-fields-v1') AND (field<>'sortTitle' OR nfo_origin->>'projection' IN ('five-field-projection-v1','extended-text-fields-v1')) AND (field NOT IN ('tagline','outline','mpaa','certification') OR nfo_origin->>'projection'='extended-text-fields-v1')
  AND jsonb_typeof(nfo_origin->'locked')='boolean'
  AND jsonb_typeof(nfo_origin->'readAt')='string'
  AND isfinite((nfo_origin->>'readAt')::timestamptz)
  AND (nfo_origin->>'readAt')::timestamptz >= timestamptz '0001-01-01 00:00:00+00'
  AND (nfo_origin->>'readAt')::timestamptz <= timestamptz '9999-12-31 23:59:59.999999+00'
 ,false))
 OR (source<>'nfo' AND nfo_origin IS NULL)
);

ALTER TABLE item_nfo_field_locks DROP CONSTRAINT item_nfo_field_locks_field_check;
ALTER TABLE item_nfo_field_locks ADD CONSTRAINT item_nfo_field_locks_field_check CHECK(field IN ('title','originalTitle','overview','date','sortTitle','tagline','outline','mpaa','certification'));
ALTER TABLE item_nfo_field_locks DROP CONSTRAINT item_nfo_field_lock_check;
ALTER TABLE item_nfo_field_locks ADD CONSTRAINT item_nfo_field_lock_check CHECK(COALESCE(
  octet_length(origin::text)<=2048 AND jsonb_typeof(origin)='object'
  AND origin ?& ARRAY['sourceId','rootId','generation','stamp','identityDigest','projection','readAt','locked']
  AND origin - ARRAY['sourceId','rootId','generation','stamp','identityDigest','projection','readAt','locked']='{}'::jsonb
  AND jsonb_typeof(origin->'locked')='boolean' AND origin->'locked'='true'::jsonb
  AND jsonb_typeof(origin->'projection')='string' AND origin->>'projection' IN ('four-text-fields-v1','lock-only-fields-v1','five-field-projection-v1','extended-text-fields-v1')
  AND (field<>'sortTitle' OR origin->>'projection' IN ('five-field-projection-v1','extended-text-fields-v1')) AND (field NOT IN ('tagline','outline','mpaa','certification') OR origin->>'projection'='extended-text-fields-v1')
  AND jsonb_typeof(origin->'sourceId')='string' AND origin->>'sourceId' ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
  AND jsonb_typeof(origin->'rootId')='string' AND origin->>'rootId' ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
  AND jsonb_typeof(origin->'generation')='number' AND origin->>'generation' ~ '^[1-9][0-9]{0,18}$' AND (origin->>'generation')::numeric <= 9223372036854775807
  AND jsonb_typeof(origin->'identityDigest')='string' AND origin->>'identityDigest' ~ '^[0-9a-f]{64}$'
  AND jsonb_typeof(origin->'readAt')='string'
  AND origin->>'readAt' ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?Z$'
  AND isfinite((origin->>'readAt')::timestamptz)
  AND (origin->>'readAt')::timestamptz >= timestamptz '0001-01-01 00:00:00+00'
  AND (origin->>'readAt')::timestamptz <= timestamptz '9999-12-31 23:59:59.999999+00'
    AND jsonb_typeof(origin->'stamp')='object'
    AND origin->'stamp' ?& ARRAY['size','modifiedUnixNano','sha256','fingerprintVersion']
    AND (origin->'stamp') - ARRAY['size','modifiedUnixNano','sha256','fingerprintVersion']='{}'::jsonb
    AND jsonb_typeof(origin->'stamp'->'size')='number' AND origin->'stamp'->>'size' ~ '^(0|[1-9][0-9]{0,7})$' AND (origin->'stamp'->>'size')::numeric <= 33554432
    AND jsonb_typeof(origin->'stamp'->'modifiedUnixNano')='number' AND origin->'stamp'->>'modifiedUnixNano' ~ '^-?(0|[1-9][0-9]{0,18})$' AND (origin->'stamp'->>'modifiedUnixNano')::numeric BETWEEN -9223372036854775808 AND 9223372036854775807
    AND jsonb_typeof(origin->'stamp'->'sha256')='string' AND origin->'stamp'->>'sha256' ~ '^[0-9a-f]{64}$'
    AND jsonb_typeof(origin->'stamp'->'fingerprintVersion')='string' AND origin->'stamp'->>'fingerprintVersion'='sha256-full-v1'
 ,false));

ALTER TABLE item_metadata_fields DROP CONSTRAINT item_metadata_sort_provider_check;
ALTER TABLE item_metadata_fields ADD CONSTRAINT item_metadata_sort_provider_check CHECK(field IN ('title','originalTitle','overview','date') OR source<>'tmdb');
COMMIT;
