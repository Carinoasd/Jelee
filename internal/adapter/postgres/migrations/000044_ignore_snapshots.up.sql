ALTER TABLE inventory_snapshot_preparations
 ADD COLUMN publication_mode text NOT NULL DEFAULT '' CHECK(publication_mode IN ('','jeleeignore','jeleeignore-legacy-v1')),
 ADD COLUMN excluded_files bigint NOT NULL DEFAULT 0 CHECK(excluded_files BETWEEN 0 AND 500000),
 ADD COLUMN excluded_copied bigint NOT NULL DEFAULT 0 CHECK(excluded_copied BETWEEN 0 AND 500000),
 ADD COLUMN excluded_after_root uuid,
 ADD COLUMN excluded_after_path text COLLATE "C" NOT NULL DEFAULT '',
 ADD CONSTRAINT inventory_snapshot_excluded_bound CHECK(excluded_copied<=excluded_files AND source_files+excluded_files<=500000),
 ADD CONSTRAINT inventory_snapshot_excluded_ready CHECK(NOT ready OR excluded_copied=excluded_files),
 ADD CONSTRAINT inventory_snapshot_plain_mode CHECK(publication_mode<>'' OR (excluded_files=0 AND excluded_copied=0 AND excluded_after_root IS NULL AND excluded_after_path=''));
