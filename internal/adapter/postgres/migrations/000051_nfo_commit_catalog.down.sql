BEGIN;
LOCK TABLE nfo_write_commit_journal,nfo_write_commit_file_plans,nfo_write_commit_files_ready IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM nfo_write_commit_journal) THEN
  RAISE EXCEPTION 'nfo catalog recovery guard is retained' USING ERRCODE='23514';
 END IF;
END $$;
DROP TRIGGER guard_nfo_catalog_root_mutation ON library_roots;
DROP TRIGGER guard_nfo_catalog_library_mutation ON libraries;
DROP TRIGGER guard_nfo_catalog_item_mutation ON items;
DROP FUNCTION guard_nfo_catalog_mutation();
DROP FUNCTION nfo_commit_xmin_is_current(xid);
DROP TRIGGER verify_nfo_commit_catalog_ready ON nfo_write_commit_files_ready;
DROP TRIGGER verify_nfo_commit_catalog_plan ON nfo_write_commit_file_plans;
DROP TRIGGER verify_nfo_commit_catalog_journal ON nfo_write_commit_journal;
DROP TRIGGER guard_nfo_commit_catalog_ready ON nfo_write_commit_files_ready;
DROP TRIGGER guard_nfo_commit_catalog_plan ON nfo_write_commit_file_plans;
DROP TRIGGER guard_nfo_commit_catalog_journal ON nfo_write_commit_journal;
DROP FUNCTION guard_nfo_commit_catalog();
DROP FUNCTION check_nfo_commit_catalog(text,uuid,integer);
DROP TRIGGER touch_nfo_catalog_revision ON item_metadata_state;
DROP TRIGGER touch_nfo_catalog_directory ON item_directory_sources;
DROP TRIGGER touch_nfo_catalog_media ON media_sources;
DROP FUNCTION touch_nfo_catalog_item();
COMMIT;
