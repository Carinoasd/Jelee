BEGIN;
-- G20.1, G20.5, G21.2: automatic version grouping across directories.
-- A versions phase runs after the pending phase of every synchronisation;
-- it folds synchronised items whose NFO provider IDs, episode numbers and
-- probed runtimes agree into one item, as an undoable merge without actor.
ALTER TABLE catalog_sync_requests DROP CONSTRAINT catalog_sync_requests_phase_check;
ALTER TABLE catalog_sync_requests ADD CONSTRAINT catalog_sync_requests_phase_check CHECK(phase IN ('publish','sources','missing','pending','versions','done'));
ALTER TABLE catalog_sync_requests ADD COLUMN versions_cursor uuid;
ALTER TABLE catalog_sync_requests ADD COLUMN aggregated bigint NOT NULL DEFAULT 0 CHECK(aggregated>=0);

-- The digest of the evidence an item was last judged on and the outcome.
-- NULL (every item of an existing library) is judged on the next run.
ALTER TABLE catalog_scan_items ADD COLUMN grouping_evidence bytea CHECK(octet_length(grouping_evidence)=32);
ALTER TABLE catalog_scan_items ADD COLUMN grouping_reason text CHECK(grouping_reason ~ '^[a-z_]{1,64}$');

-- An automatic merge has no actor; an administrator's merge keeps false
-- even when the account is deleted later.
ALTER TABLE item_version_operations ADD COLUMN automatic boolean NOT NULL DEFAULT false;
ALTER TABLE item_version_operations ADD CONSTRAINT item_version_operations_automatic_check CHECK(NOT automatic OR kind='merge' AND actor_id IS NULL);

CREATE INDEX catalog_scan_items_versions_idx ON catalog_scan_items(library_id,item_id) WHERE kind IN ('Movie','Episode');
CREATE INDEX item_metadata_facts_nfo_unique_ids_idx ON item_metadata_facts USING gin(value jsonb_path_ops) WHERE field='uniqueIds' AND source='nfo';
COMMIT;
