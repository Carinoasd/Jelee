BEGIN;
-- Attempts are only a "done for this file" memo; dropping them makes every
-- item with an embedded cover a candidate again. Stored item_images rows
-- (source_kind 'embedded') already existed in schema 58 and stay.
DROP TABLE item_embedded_cover_attempts;
COMMIT;
