BEGIN;
LOCK TABLE item_identity_corrections,catalog_scan_pending IN ACCESS EXCLUSIVE MODE;
-- The previous schema would let the next synchronisation overwrite locked
-- identification and turn rejected files into items. Unlock and reconsider
-- first.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM item_identity_corrections WHERE kind_locked OR parent_locked OR season IS NOT NULL OR episode IS NOT NULL)
  OR EXISTS(SELECT 1 FROM catalog_scan_pending WHERE rejected_at IS NOT NULL) THEN
  RAISE EXCEPTION 'retained identity corrections or pending rejections prevent downgrade' USING ERRCODE='55000';
 END IF;
END $$;
ALTER TABLE catalog_scan_pending DROP CONSTRAINT catalog_scan_pending_rejected_check;
ALTER TABLE catalog_scan_pending DROP COLUMN rejected_by;
ALTER TABLE catalog_scan_pending DROP COLUMN rejected_at;
DROP TABLE item_identity_corrections;
COMMIT;
