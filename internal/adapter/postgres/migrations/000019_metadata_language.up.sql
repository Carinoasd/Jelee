BEGIN;
ALTER TABLE libraries
 ADD COLUMN metadata_language text NOT NULL DEFAULT 'zh-CN'
 CHECK (metadata_language IN ('zh-CN','zh-TW','ja-JP','en-US')),
 ADD COLUMN metadata_preferences_revision bigint NOT NULL DEFAULT 1
 CHECK (metadata_preferences_revision BETWEEN 1 AND 2147483647);
COMMIT;
