BEGIN;
-- Attempts are only a "done for this file" memo; dropping them makes every
-- item with chapter pictures a candidate again. Stored item_images rows
-- (image_type 'Chapter', source_kind 'embedded') were valid in schema 58 and
-- stay.
DROP TABLE item_chapter_image_attempts;
COMMIT;
