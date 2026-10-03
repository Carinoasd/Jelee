DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM libraries WHERE active_inventory_snapshot<>0)
 OR EXISTS(SELECT 1 FROM library_inventory_baseline_data WHERE snapshot_id<>0)
 OR EXISTS(SELECT 1 FROM inventory_snapshot_preparations) THEN
  RAISE EXCEPTION 'retained inventory snapshots prevent downgrade';
 END IF;
END $$;
DROP TABLE inventory_snapshot_preparations;
DROP VIEW library_inventory_baseline;
DROP TRIGGER baseline_default_snapshot ON library_inventory_baseline_data;
DROP FUNCTION baseline_default_snapshot();
ALTER TABLE library_inventory_baseline_data DROP CONSTRAINT library_inventory_baseline_pkey;
ALTER TABLE library_inventory_baseline_data DROP COLUMN snapshot_id;
ALTER TABLE library_inventory_baseline_data ADD CONSTRAINT library_inventory_baseline_pkey PRIMARY KEY(library_id,root_id,path);
ALTER TABLE library_inventory_baseline_data RENAME TO library_inventory_baseline;
DROP SEQUENCE inventory_snapshot_sequence;
ALTER TABLE libraries DROP COLUMN active_inventory_snapshot;
