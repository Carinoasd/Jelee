BEGIN;
LOCK TABLE nfo_write_preparations,nfo_write_entries,nfo_write_commit_journal IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM nfo_write_preparations WHERE root_generation IS NOT NULL)
  OR EXISTS(SELECT 1 FROM nfo_write_entries WHERE root_generation IS NOT NULL)
  OR EXISTS(SELECT 1 FROM nfo_write_commit_journal) THEN
  RAISE EXCEPTION 'nfo write root observations are retained' USING ERRCODE='23514';
 END IF;
END $$;
DROP TRIGGER guard_nfo_write_root_mutation ON library_roots;
DROP FUNCTION guard_nfo_write_root_mutation();
DROP FUNCTION check_nfo_commit_catalog(text,uuid,integer);
ALTER FUNCTION check_nfo_commit_catalog_v51(text,uuid,integer) RENAME TO check_nfo_commit_catalog;
DROP TRIGGER verify_nfo_write_entry_root ON nfo_write_entries;
DROP TRIGGER verify_nfo_write_preparation_root ON nfo_write_preparations;
DROP TRIGGER guard_nfo_write_entry_root ON nfo_write_entries;
DROP TRIGGER guard_nfo_write_preparation_root ON nfo_write_preparations;
DROP FUNCTION guard_nfo_write_root_generation();
ALTER TABLE nfo_write_entries DROP COLUMN root_generation;
ALTER TABLE nfo_write_preparations DROP COLUMN root_generation;
COMMIT;
