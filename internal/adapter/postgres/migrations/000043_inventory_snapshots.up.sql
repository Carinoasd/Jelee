ALTER TABLE libraries ADD COLUMN active_inventory_snapshot bigint NOT NULL DEFAULT 0 CHECK(active_inventory_snapshot>=0);
CREATE SEQUENCE inventory_snapshot_sequence AS bigint MINVALUE 1;
ALTER TABLE library_inventory_baseline RENAME TO library_inventory_baseline_data;
ALTER TABLE library_inventory_baseline_data ADD COLUMN snapshot_id bigint NOT NULL DEFAULT 0 CHECK(snapshot_id>=0);
ALTER TABLE library_inventory_baseline_data DROP CONSTRAINT library_inventory_baseline_pkey;
ALTER TABLE library_inventory_baseline_data ADD CONSTRAINT library_inventory_baseline_pkey PRIMARY KEY(library_id,snapshot_id,root_id,path);
CREATE INDEX library_inventory_snapshot_cursor_idx ON library_inventory_baseline_data(library_id,snapshot_id,root_id,path COLLATE "C");
-- Keep the existing path-ordered cursor index as well as the snapshot key.
-- It lets bounded public cursors preserve ordering through the visibility view.
CREATE FUNCTION baseline_default_snapshot() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.snapshot_id=0 THEN
  SELECT active_inventory_snapshot INTO NEW.snapshot_id FROM libraries WHERE id=NEW.library_id;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER baseline_default_snapshot BEFORE INSERT ON library_inventory_baseline_data FOR EACH ROW EXECUTE FUNCTION baseline_default_snapshot();
CREATE VIEW library_inventory_baseline AS
 SELECT library_id,root_id,path,attributes_known,kind,size,modified_unix_nano,inventory_generation,observed_revision
 FROM library_inventory_baseline_data b
 WHERE EXISTS(SELECT 1 FROM libraries l WHERE l.id=b.library_id AND l.active_inventory_snapshot=b.snapshot_id)
 WITH LOCAL CHECK OPTION;
CREATE TABLE inventory_snapshot_preparations (
 job_id uuid PRIMARY KEY,
 library_id uuid NOT NULL,
 snapshot_id bigint NOT NULL UNIQUE DEFAULT nextval('inventory_snapshot_sequence'),
 previous_snapshot bigint NOT NULL CHECK(previous_snapshot>=0),
 baseline_revision bigint NOT NULL CHECK(baseline_revision>0),
 inventory_generation bigint NOT NULL CHECK(inventory_generation>0),
 source_files bigint NOT NULL CHECK(source_files BETWEEN 0 AND 500000),
 cursor_id uuid,
 copied bigint NOT NULL DEFAULT 0 CHECK(copied BETWEEN 0 AND 500000),
 cleaned boolean NOT NULL DEFAULT false,
 ready boolean NOT NULL DEFAULT false,
 FOREIGN KEY(job_id,library_id) REFERENCES jobs(id,library_id) ON DELETE CASCADE,
 CHECK(copied<=source_files),
 CHECK(NOT ready OR copied=source_files)
);
