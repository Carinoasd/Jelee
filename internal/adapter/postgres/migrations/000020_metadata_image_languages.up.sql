BEGIN;
ALTER TABLE libraries ADD COLUMN metadata_image_languages text[] NOT NULL
 DEFAULT ARRAY['zh','ja','en','null']::text[]
 CHECK (
  array_ndims(metadata_image_languages)=1
  AND array_lower(metadata_image_languages,1)=1
  AND cardinality(metadata_image_languages) BETWEEN 1 AND 4
  AND array_position(metadata_image_languages,NULL) IS NULL
  AND metadata_image_languages <@ ARRAY['zh','ja','en','null']::text[]
  AND cardinality(array_positions(metadata_image_languages,'zh'))<=1
  AND cardinality(array_positions(metadata_image_languages,'ja'))<=1
  AND cardinality(array_positions(metadata_image_languages,'en'))<=1
  AND cardinality(array_positions(metadata_image_languages,'null'))<=1
 );
COMMIT;
