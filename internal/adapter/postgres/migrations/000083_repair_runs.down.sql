BEGIN;
LOCK TABLE repair_runs,repair_journal IN ACCESS EXCLUSIVE MODE;
-- Repair runs and their journal are retained state: an older binary could
-- neither list nor revert them.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM repair_runs) OR EXISTS(SELECT 1 FROM repair_journal) THEN
  RAISE EXCEPTION 'retained repair state prevents downgrade' USING ERRCODE='55000';
 END IF;
END $$;
DROP TABLE repair_journal;
DROP TABLE repair_runs;
COMMIT;
