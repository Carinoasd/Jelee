BEGIN;
-- Favourites (G24.2): the items each user marked as a favourite. A row
-- belongs to one user and one logical item; every version of the item
-- shares it, like user_item_data. The composite key on (item_id,library_id)
-- keeps the row in the item's library, so the unified visibility filter can
-- bind it by library (G48.3). Rows go with their user or item; every read
-- and every change binds the item to the caller through the visibility
-- filter in the same statement, so a hidden item is never listed and naming
-- one is answered like a missing item.
CREATE TABLE user_favorites (
 user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 item_id uuid NOT NULL,
 library_id uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp() CONSTRAINT user_favorites_created_at_check CHECK(isfinite(created_at)),
 PRIMARY KEY(user_id,item_id),
 FOREIGN KEY(item_id,library_id) REFERENCES items(id,library_id) ON DELETE CASCADE
);
-- The favourites list pages a user's rows newest first.
CREATE INDEX user_favorites_user_idx ON user_favorites(user_id,created_at DESC,item_id DESC);
CREATE INDEX user_favorites_item_idx ON user_favorites(item_id,library_id);

-- The legacy import ledger (G04.6) records imported favourites under their
-- own kind, so verification can count them apart from progress rows.
ALTER TABLE legacy_import_map DROP CONSTRAINT legacy_import_map_kind_check;
ALTER TABLE legacy_import_map ADD CONSTRAINT legacy_import_map_kind_check CHECK(kind IN ('user','library','item','user_data','favorite'));
ALTER TABLE legacy_import_map DROP CONSTRAINT legacy_import_map_user_check;
ALTER TABLE legacy_import_map ADD CONSTRAINT legacy_import_map_user_check CHECK((kind IN ('user_data','favorite'))=(target_user IS NOT NULL));
COMMIT;
