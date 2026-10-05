BEGIN;
-- Schema 64 pairs nothing during catalog sync. Stored sidecar rows stay; they
-- only lose the lookup paths that kept them current.
DROP INDEX media_sidecar_tracks_uninspected_idx;
DROP INDEX library_inventory_sidecar_owner_idx;
DROP FUNCTION inventory_sidecar_owner(text);
COMMIT;
