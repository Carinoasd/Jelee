BEGIN;
LOCK TABLE item_nfo_field_locks IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM item_nfo_field_locks WHERE origin->>'projection'='lock-only-fields-v1') THEN
  RAISE EXCEPTION 'retained lock-only NFO proof prevents rollback' USING ERRCODE='55000';
 END IF;
END $$;
ALTER TABLE item_nfo_field_locks DROP CONSTRAINT item_nfo_field_lock_check;
ALTER TABLE item_nfo_field_locks ADD CONSTRAINT item_nfo_field_lock_check CHECK(COALESCE(
  octet_length(origin::text)<=2048 AND jsonb_typeof(origin)='object'
  AND origin ?& ARRAY['sourceId','rootId','generation','stamp','identityDigest','projection','readAt','locked']
  AND origin - ARRAY['sourceId','rootId','generation','stamp','identityDigest','projection','readAt','locked']='{}'::jsonb
  AND jsonb_typeof(origin->'locked')='boolean' AND origin->'locked'='true'::jsonb
  AND jsonb_typeof(origin->'projection')='string' AND origin->>'projection'='four-text-fields-v1'
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
COMMIT;
