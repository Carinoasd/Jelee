BEGIN;
LOCK TABLE user_favorites, legacy_import_map IN ACCESS EXCLUSIVE MODE;
-- Schema 110 has nowhere to keep favourites, which are user data, so stored
-- rows block the downgrade like progress and playlists do; deleting them
-- first is an explicit operator decision.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM user_favorites) THEN
  RAISE EXCEPTION 'favourites prevent downgrade' USING ERRCODE='55000';
 END IF;
END $$;
-- Ledger rows of imported favourites only name rows that no longer exist.
DELETE FROM legacy_import_map WHERE kind='favorite';
ALTER TABLE legacy_import_map DROP CONSTRAINT legacy_import_map_user_check;
ALTER TABLE legacy_import_map ADD CONSTRAINT legacy_import_map_user_check CHECK((kind='user_data')=(target_user IS NOT NULL));
ALTER TABLE legacy_import_map DROP CONSTRAINT legacy_import_map_kind_check;
ALTER TABLE legacy_import_map ADD CONSTRAINT legacy_import_map_kind_check CHECK(kind IN ('user','library','item','user_data'));
DROP TABLE user_favorites;
COMMIT;
