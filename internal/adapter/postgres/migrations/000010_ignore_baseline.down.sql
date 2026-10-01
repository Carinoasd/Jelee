BEGIN;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM job_ignore_comparisons) THEN
  RAISE EXCEPTION 'retained ignore comparison prevents rollback' USING ERRCODE='55000';
 END IF;
 IF EXISTS(SELECT 1 FROM library_inventory_baseline b JOIN libraries l ON l.id=b.library_id WHERE b.observed_revision<>l.inventory_baseline_revision) THEN
  RAISE EXCEPTION 'protected baseline provenance prevents rollback' USING ERRCODE='55000';
 END IF;
END $$;
DROP TRIGGER job_ignore_directories_frozen ON job_directories;
DROP TRIGGER job_ignore_inventory_frozen ON job_inventory;
DROP FUNCTION job_ignore_inventory_frozen();
DROP TABLE job_ignore_comparison_pages;
DROP TABLE job_ignore_decisions;
DROP TABLE job_ignore_comparisons;
DROP INDEX library_inventory_baseline_cursor_idx;
ALTER TABLE library_inventory_baseline DROP COLUMN observed_revision;
ALTER TABLE libraries DROP COLUMN inventory_baseline_revision;
COMMIT;
