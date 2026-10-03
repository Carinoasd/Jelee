DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM inventory_snapshot_preparations WHERE publication_mode<>'') THEN
  RAISE EXCEPTION 'retained ignored inventory snapshots prevent downgrade';
 END IF;
END $$;
ALTER TABLE inventory_snapshot_preparations
 DROP CONSTRAINT inventory_snapshot_plain_mode,
 DROP CONSTRAINT inventory_snapshot_excluded_ready,
 DROP CONSTRAINT inventory_snapshot_excluded_bound,
 DROP COLUMN excluded_after_path,
 DROP COLUMN excluded_after_root,
 DROP COLUMN excluded_copied,
 DROP COLUMN excluded_files,
 DROP COLUMN publication_mode;
