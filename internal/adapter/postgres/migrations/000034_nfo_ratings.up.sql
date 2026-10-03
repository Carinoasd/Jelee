BEGIN;
CREATE FUNCTION valid_item_metadata_ratings(v jsonb) RETURNS boolean
LANGUAGE plpgsql IMMUTABLE STRICT AS $$
DECLARE e jsonb; total_bytes integer := 0; maximum numeric; score numeric;
BEGIN
 IF jsonb_typeof(v)<>'array' OR octet_length(v::text)>131072 THEN RETURN false; END IF;
 IF jsonb_array_length(v)>128 THEN RETURN false; END IF;
 FOR e IN SELECT value FROM jsonb_array_elements(v) LOOP
  IF jsonb_typeof(e)<>'object' THEN RETURN false; END IF;
  IF NOT e ? 'value' OR e - ARRAY['name','value','max','votes','default'] <> '{}'::jsonb THEN RETURN false; END IF;
  IF e ? 'name' THEN
   IF jsonb_typeof(e->'name')<>'string' OR octet_length(e->>'name')>1024 THEN RETURN false; END IF;
   total_bytes:=total_bytes+octet_length(e->>'name');
   IF total_bytes>16384 THEN RETURN false; END IF;
  END IF;
  maximum:=10;
  IF e ? 'max' AND e->'max'<>'null'::jsonb THEN
   IF jsonb_typeof(e->'max')<>'number' THEN RETURN false; END IF;
   maximum:=(e->>'max')::numeric;
   IF maximum<=0 OR maximum>1000000 THEN RETURN false; END IF;
  END IF;
  IF jsonb_typeof(e->'value')<>'number' THEN RETURN false; END IF;
  score:=(e->>'value')::numeric;
  IF score<0 OR score>maximum THEN RETURN false; END IF;
  IF e ? 'votes' AND e->'votes'<>'null'::jsonb THEN
   IF jsonb_typeof(e->'votes')<>'number' OR e->>'votes' !~ '^[0-9]{1,10}$' THEN RETURN false; END IF;
   IF (e->>'votes')::numeric>2147483647 THEN RETURN false; END IF;
  END IF;
  IF e ? 'default' AND jsonb_typeof(e->'default')<>'boolean' THEN RETURN false; END IF;
 END LOOP;
 RETURN true;
END $$;
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
  AND jsonb_typeof(nfo_origin->'projection')='string' AND nfo_origin->>'projection' IN ('four-text-fields-v1','five-field-projection-v1','extended-text-fields-v1','year-fact-v1','numeric-facts-v1','string-lists-v1','actor-structure-v1','provider-identifiers-v1','multi-source-ratings-v1') AND (field<>'sortTitle' OR nfo_origin->>'projection' IN ('five-field-projection-v1','extended-text-fields-v1','year-fact-v1','numeric-facts-v1','string-lists-v1','actor-structure-v1','provider-identifiers-v1','multi-source-ratings-v1')) AND (field NOT IN ('tagline','outline','mpaa','certification') OR nfo_origin->>'projection' IN ('extended-text-fields-v1','year-fact-v1','numeric-facts-v1','string-lists-v1','actor-structure-v1','provider-identifiers-v1','multi-source-ratings-v1'))
  AND (field<>'actors' OR nfo_origin->>'projection' IN ('actor-structure-v1','provider-identifiers-v1','multi-source-ratings-v1'))
  AND (field<>'uniqueIds' OR nfo_origin->>'projection' IN ('provider-identifiers-v1','multi-source-ratings-v1'))
  AND (field<>'ratings' OR nfo_origin->>'projection'='multi-source-ratings-v1')
  AND jsonb_typeof(nfo_origin->'locked')='boolean'
  AND jsonb_typeof(nfo_origin->'readAt')='string'
  AND isfinite((nfo_origin->>'readAt')::timestamptz)
  AND (nfo_origin->>'readAt')::timestamptz >= timestamptz '0001-01-01 00:00:00+00'
  AND (nfo_origin->>'readAt')::timestamptz <= timestamptz '9999-12-31 23:59:59.999999+00'
 ,false))
 OR (source<>'nfo' AND nfo_origin IS NULL)
);

ALTER TABLE item_nfo_field_locks DROP CONSTRAINT item_nfo_field_locks_field_check;
ALTER TABLE item_nfo_field_locks ADD CONSTRAINT item_nfo_field_locks_field_check CHECK(field IN ('ratings','uniqueIds','actors','title','originalTitle','overview','date','sortTitle','tagline','outline','mpaa','certification','year','runtimeMinutes','rating','userRating','genres','tags','studios','countries','languages','directors','writers','producers'));
ALTER TABLE item_nfo_field_locks DROP CONSTRAINT item_nfo_field_lock_check;
ALTER TABLE item_nfo_field_locks ADD CONSTRAINT item_nfo_field_lock_check CHECK(COALESCE(
  octet_length(origin::text)<=2048 AND jsonb_typeof(origin)='object'
  AND origin ?& ARRAY['sourceId','rootId','generation','stamp','identityDigest','projection','readAt','locked']
  AND origin - ARRAY['sourceId','rootId','generation','stamp','identityDigest','projection','readAt','locked']='{}'::jsonb
  AND (field<>'actors' OR origin->>'projection' IN ('actor-structure-v1','provider-identifiers-v1','multi-source-ratings-v1'))
  AND (field<>'uniqueIds' OR origin->>'projection' IN ('provider-identifiers-v1','multi-source-ratings-v1'))
  AND (field<>'ratings' OR origin->>'projection'='multi-source-ratings-v1')
  AND jsonb_typeof(origin->'locked')='boolean' AND origin->'locked'='true'::jsonb
  AND jsonb_typeof(origin->'projection')='string' AND origin->>'projection' IN ('four-text-fields-v1','lock-only-fields-v1','five-field-projection-v1','extended-text-fields-v1','year-fact-v1','numeric-facts-v1','string-lists-v1','actor-structure-v1','provider-identifiers-v1','multi-source-ratings-v1')
  AND (field<>'userRating' OR origin->>'projection' IN ('numeric-facts-v1','string-lists-v1','actor-structure-v1','provider-identifiers-v1','multi-source-ratings-v1'))
  AND (field<>'rating' OR origin->>'projection' IN ('numeric-facts-v1','string-lists-v1','actor-structure-v1','provider-identifiers-v1','multi-source-ratings-v1'))
  AND (field<>'runtimeMinutes' OR origin->>'projection' IN ('numeric-facts-v1','string-lists-v1','actor-structure-v1','provider-identifiers-v1','multi-source-ratings-v1'))
  AND (field NOT IN ('genres','tags','studios','countries','languages','directors','writers','producers') OR origin->>'projection' IN ('string-lists-v1','actor-structure-v1','provider-identifiers-v1','multi-source-ratings-v1'))
  AND (field<>'year' OR origin->>'projection' IN ('year-fact-v1','numeric-facts-v1','string-lists-v1','actor-structure-v1','provider-identifiers-v1','multi-source-ratings-v1'))
  AND (field<>'sortTitle' OR origin->>'projection' IN ('five-field-projection-v1','extended-text-fields-v1','year-fact-v1','numeric-facts-v1','string-lists-v1','actor-structure-v1','provider-identifiers-v1','multi-source-ratings-v1')) AND (field NOT IN ('tagline','outline','mpaa','certification') OR origin->>'projection' IN ('extended-text-fields-v1','year-fact-v1','numeric-facts-v1','string-lists-v1','actor-structure-v1','provider-identifiers-v1','multi-source-ratings-v1'))
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

ALTER TABLE item_metadata_facts DROP CONSTRAINT item_metadata_facts_field_check;
ALTER TABLE item_metadata_facts ADD CONSTRAINT item_metadata_facts_field_check CHECK(field IN ('ratings','uniqueIds','actors','year','runtimeMinutes','rating','userRating','genres','tags','studios','countries','languages','directors','writers','producers'));
ALTER TABLE item_metadata_facts DROP CONSTRAINT item_metadata_fact_value_check;
ALTER TABLE item_metadata_facts ADD CONSTRAINT item_metadata_fact_value_check CHECK(
COALESCE(CASE
 WHEN value='null'::jsonb THEN source<>'nfo'
 WHEN field IN ('genres','tags','studios','countries','languages','directors','writers','producers') THEN CASE WHEN valid_item_metadata_string_list(value) THEN source<>'nfo' OR jsonb_array_length(value)>0 ELSE false END
 WHEN field='actors' THEN CASE WHEN valid_item_metadata_actors(value) THEN source<>'nfo' OR jsonb_array_length(value)>0 ELSE false END
 WHEN field='uniqueIds' THEN CASE WHEN valid_item_metadata_unique_ids(value) THEN source<>'nfo' OR jsonb_array_length(value)>0 ELSE false END
 WHEN field='ratings' THEN CASE WHEN valid_item_metadata_ratings(value) THEN source<>'nfo' OR jsonb_array_length(value)>0 ELSE false END
 WHEN jsonb_typeof(value)<>'number' THEN false
 WHEN field='year' THEN CASE WHEN value::text ~ '^[1-9][0-9]{0,3}$' THEN (value::text)::numeric BETWEEN 1 AND 9999 ELSE false END
 WHEN field='runtimeMinutes' THEN CASE WHEN value::text ~ '^(0|[1-9][0-9]{0,7})$' THEN (value::text)::numeric BETWEEN 0 AND 10000000 ELSE false END
 WHEN field IN ('rating','userRating') THEN octet_length(value::text)<=1024 AND (value::text)::numeric BETWEEN 0 AND 10
 ELSE false END,false)
);
ALTER TABLE item_metadata_facts DROP CONSTRAINT item_metadata_fact_nfo_origin_check;
ALTER TABLE item_metadata_facts ADD CONSTRAINT item_metadata_fact_nfo_origin_check CHECK(
 octet_length(COALESCE(nfo_origin::text,''))<=2048 AND ( (source='nfo' AND nfo_origin IS NOT NULL AND COALESCE(
  jsonb_typeof(nfo_origin)='object'
  AND nfo_origin ?& ARRAY['sourceId','rootId','generation','sha256','identityDigest','projection','readAt','locked']
  AND nfo_origin - ARRAY['sourceId','rootId','generation','sha256','identityDigest','projection','readAt','locked'] = '{}'::jsonb
  AND jsonb_typeof(nfo_origin->'sourceId')='string' AND (nfo_origin->>'sourceId') ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
  AND jsonb_typeof(nfo_origin->'rootId')='string' AND (nfo_origin->>'rootId') ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
  AND jsonb_typeof(nfo_origin->'generation')='number' AND (nfo_origin->>'generation') ~ '^[1-9][0-9]{0,18}$' AND (nfo_origin->>'generation')::numeric <= 9223372036854775807
  AND jsonb_typeof(nfo_origin->'sha256')='string' AND (nfo_origin->>'sha256') ~ '^[0-9a-f]{64}$'
  AND jsonb_typeof(nfo_origin->'identityDigest')='string' AND (nfo_origin->>'identityDigest') ~ '^[0-9a-f]{64}$'
  AND jsonb_typeof(nfo_origin->'projection')='string' AND nfo_origin->>'projection' IN ('year-fact-v1','numeric-facts-v1','string-lists-v1','actor-structure-v1','provider-identifiers-v1','multi-source-ratings-v1') AND (field='year' OR nfo_origin->>'projection' IN ('numeric-facts-v1','string-lists-v1','actor-structure-v1','provider-identifiers-v1','multi-source-ratings-v1')) AND (field<>'sortTitle' OR nfo_origin->>'projection' IN ('five-field-projection-v1','extended-text-fields-v1','year-fact-v1','numeric-facts-v1','string-lists-v1','actor-structure-v1','provider-identifiers-v1','multi-source-ratings-v1')) AND (field NOT IN ('tagline','outline','mpaa','certification') OR nfo_origin->>'projection' IN ('extended-text-fields-v1','year-fact-v1','numeric-facts-v1','string-lists-v1','actor-structure-v1','provider-identifiers-v1','multi-source-ratings-v1'))
  AND (field NOT IN ('genres','tags','studios','countries','languages','directors','writers','producers') OR nfo_origin->>'projection' IN ('string-lists-v1','actor-structure-v1','provider-identifiers-v1','multi-source-ratings-v1'))
  AND (field<>'actors' OR nfo_origin->>'projection' IN ('actor-structure-v1','provider-identifiers-v1','multi-source-ratings-v1'))
  AND (field<>'uniqueIds' OR nfo_origin->>'projection' IN ('provider-identifiers-v1','multi-source-ratings-v1'))
  AND (field<>'ratings' OR nfo_origin->>'projection'='multi-source-ratings-v1')
  AND jsonb_typeof(nfo_origin->'locked')='boolean'
  AND jsonb_typeof(nfo_origin->'readAt')='string'
  AND isfinite((nfo_origin->>'readAt')::timestamptz)
  AND (nfo_origin->>'readAt')::timestamptz >= timestamptz '0001-01-01 00:00:00+00'
  AND (nfo_origin->>'readAt')::timestamptz <= timestamptz '9999-12-31 23:59:59.999999+00'
 ,false))
 OR (source<>'nfo' AND nfo_origin IS NULL) )
);
COMMIT;
