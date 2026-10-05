BEGIN;
LOCK TABLE legacy_import_runs,legacy_import_checkpoints,legacy_import_map,legacy_import_pending IN ACCESS EXCLUSIVE MODE;
-- The ledger is what keeps a repeated import from duplicating accounts and
-- libraries; an older binary would drop it silently.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM legacy_import_runs) OR EXISTS(SELECT 1 FROM legacy_import_map) THEN
  RAISE EXCEPTION 'retained legacy import state prevents downgrade' USING ERRCODE='55000';
 END IF;
END $$;
DROP TABLE legacy_import_pending;
DROP TABLE legacy_import_map;
DROP TABLE legacy_import_checkpoints;
DROP TABLE legacy_import_runs;
COMMIT;
