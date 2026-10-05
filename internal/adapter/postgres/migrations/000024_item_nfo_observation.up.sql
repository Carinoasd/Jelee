BEGIN;
CREATE TABLE item_nfo_observations (
 item_id uuid PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
 observation jsonb NOT NULL,
 CONSTRAINT item_nfo_observation_check CHECK(COALESCE(
  octet_length(observation::text) <= 2048
  AND jsonb_typeof(observation)='object'
  AND observation ?& ARRAY['version','status','sourceId','rootId','generation','identityDigest','candidateDigest','readAt','acceptedRevision','stamp']
  AND observation - ARRAY['version','status','sourceId','rootId','generation','identityDigest','candidateDigest','readAt','acceptedRevision','stamp']='{}'::jsonb
  AND observation->>'version'='nfo-item-observation-v1'
  AND jsonb_typeof(observation->'version')='string'
  AND jsonb_typeof(observation->'status')='string'
  AND observation->>'status' IN ('valid','missing','nfo_invalid')
  AND jsonb_typeof(observation->'sourceId')='string' AND observation->>'sourceId' ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
  AND jsonb_typeof(observation->'rootId')='string' AND observation->>'rootId' ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
  AND jsonb_typeof(observation->'generation')='number' AND observation->>'generation' ~ '^[1-9][0-9]{0,18}$' AND (observation->>'generation')::numeric <= 9223372036854775807
  AND jsonb_typeof(observation->'identityDigest')='string' AND observation->>'identityDigest' ~ '^[0-9a-f]{64}$'
  AND jsonb_typeof(observation->'candidateDigest')='string' AND observation->>'candidateDigest' ~ '^[0-9a-f]{64}$'
  AND jsonb_typeof(observation->'acceptedRevision')='number' AND observation->>'acceptedRevision' ~ '^[1-9][0-9]{0,9}$' AND (observation->>'acceptedRevision')::numeric BETWEEN 2 AND 2147483647
  AND jsonb_typeof(observation->'readAt')='string'
  AND observation->>'readAt' ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?Z$'
  AND isfinite((observation->>'readAt')::timestamptz)
  AND (observation->>'readAt')::timestamptz >= timestamptz '0001-01-01 00:00:00+00'
  AND (observation->>'readAt')::timestamptz <= timestamptz '9999-12-31 23:59:59.999999+00'
  AND (
   (observation->>'status'='missing' AND observation->'stamp'='null'::jsonb AND observation->>'candidateDigest'='4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945')
   OR (observation->>'status' IN ('valid','nfo_invalid')
    AND observation->>'candidateDigest'<>'4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945'
    AND jsonb_typeof(observation->'stamp')='object'
    AND observation->'stamp' ?& ARRAY['size','modifiedUnixNano','sha256','fingerprintVersion']
    AND (observation->'stamp') - ARRAY['size','modifiedUnixNano','sha256','fingerprintVersion']='{}'::jsonb
    AND jsonb_typeof(observation->'stamp'->'size')='number' AND observation->'stamp'->>'size' ~ '^(0|[1-9][0-9]{0,7})$' AND (observation->'stamp'->>'size')::numeric <= 33554432
    AND jsonb_typeof(observation->'stamp'->'modifiedUnixNano')='number' AND observation->'stamp'->>'modifiedUnixNano' ~ '^-?(0|[1-9][0-9]{0,18})$' AND (observation->'stamp'->>'modifiedUnixNano')::numeric BETWEEN -9223372036854775808 AND 9223372036854775807
    AND jsonb_typeof(observation->'stamp'->'sha256')='string' AND observation->'stamp'->>'sha256' ~ '^[0-9a-f]{64}$'
    AND jsonb_typeof(observation->'stamp'->'fingerprintVersion')='string' AND observation->'stamp'->>'fingerprintVersion'='sha256-full-v1'
   )
  )
 ,false))
);
COMMIT;
