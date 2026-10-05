BEGIN;
LOCK TABLE collections, collection_items, playlists, playlist_items IN ACCESS EXCLUSIVE MODE;
-- The previous schema has no collections or playlists. Dropping them would
-- silently lose what administrators and users built, so any stored row
-- blocks the downgrade; deleting them first is an explicit operator
-- decision. Their audit records stay.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM collections) OR EXISTS(SELECT 1 FROM playlists) THEN
  RAISE EXCEPTION 'collections or playlists prevent downgrade' USING ERRCODE='55000';
 END IF;
END $$;
DROP INDEX item_metadata_facts_collection_name_idx;
DROP TABLE playlist_items;
DROP TABLE playlists;
DROP TABLE collection_items;
DROP TABLE collections;
COMMIT;
