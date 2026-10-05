BEGIN;
ALTER TABLE item_metadata_fields DROP CONSTRAINT item_metadata_fields_source_check;
ALTER TABLE item_metadata_fields ADD CONSTRAINT item_metadata_fields_source_check CHECK(source IN ('existing','manual','tmdb'));
ALTER TABLE item_metadata_fields
 ADD COLUMN provider_resource text,
 ADD COLUMN provider_id integer,
 ADD COLUMN provider_source_url text,
 ADD COLUMN provider_language text,
 ADD COLUMN provider_fetched_at timestamptz;
ALTER TABLE item_metadata_fields ADD CONSTRAINT item_metadata_fields_provider_origin_check CHECK(
 (source='tmdb' AND provider_resource IS NOT NULL AND provider_resource IN ('movie','series') AND provider_id IS NOT NULL AND provider_id>0 AND provider_source_url IS NOT NULL AND provider_source_url='https://www.themoviedb.org/'||(CASE provider_resource WHEN 'movie' THEN 'movie' ELSE 'tv' END)||'/'||provider_id::text AND provider_language IS NOT NULL AND provider_language IN ('zh-CN','zh-TW','ja-JP','en-US') AND provider_fetched_at IS NOT NULL AND isfinite(provider_fetched_at) AND provider_fetched_at >= timestamptz '0001-01-01 00:00:00+00' AND provider_fetched_at <= timestamptz '9999-12-31 23:59:59.999999+00')
 OR (source<>'tmdb' AND provider_resource IS NULL AND provider_id IS NULL AND provider_source_url IS NULL AND provider_language IS NULL AND provider_fetched_at IS NULL)
);
COMMIT;
