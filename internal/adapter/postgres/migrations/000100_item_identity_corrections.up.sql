BEGIN;
-- G21.5: administrator corrections of the identification of an item. Kind
-- and parent live in items.kind and item_parent_links like any other
-- structure; this row only records that an administrator holds them. Season
-- and episode numbers are stored here as the locked value and win over the
-- NFO facts and the file-name values in catalog_scan_items, which scans keep
-- refreshing underneath. A correction is always locked: unlocking returns the
-- field to the scanner. The row outlives an unlock so the revision never
-- repeats.
CREATE TABLE item_identity_corrections (
 item_id uuid PRIMARY KEY,
 library_id uuid NOT NULL,
 revision integer NOT NULL CHECK(revision BETWEEN 2 AND 2147483647),
 kind_locked boolean NOT NULL DEFAULT false,
 parent_locked boolean NOT NULL DEFAULT false,
 season integer CHECK(season BETWEEN 0 AND 100000),
 episode integer CHECK(episode BETWEEN 0 AND 100000),
 episode_end integer CHECK(episode_end BETWEEN 0 AND 100000),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(item_id,library_id) REFERENCES items(id,library_id) ON DELETE CASCADE,
 CHECK((episode IS NULL)=(episode_end IS NULL)),
 CHECK(episode_end IS NULL OR episode_end>=episode)
);

-- An administrator's rejection of a pending file. It holds while the file
-- keeps the size and modification time it was rejected with; a changed file
-- is planned again.
ALTER TABLE catalog_scan_pending ADD COLUMN rejected_at timestamptz;
ALTER TABLE catalog_scan_pending ADD COLUMN rejected_by uuid REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE catalog_scan_pending ADD CONSTRAINT catalog_scan_pending_rejected_check CHECK(rejected_at IS NOT NULL OR rejected_by IS NULL);
COMMIT;
