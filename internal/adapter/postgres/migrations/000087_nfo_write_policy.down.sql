BEGIN;
LOCK TABLE libraries,jobs,nfo_write_entries IN ACCESS EXCLUSIVE MODE;
DROP FUNCTION check_nfo_commit_catalog(text,uuid,integer);
ALTER FUNCTION check_nfo_commit_catalog_v86(text,uuid,integer) RENAME TO check_nfo_commit_catalog;
ALTER TABLE libraries DROP CONSTRAINT libraries_nfo_write_needs_read;
ALTER TABLE libraries DROP COLUMN nfo_write_enabled;
COMMIT;
