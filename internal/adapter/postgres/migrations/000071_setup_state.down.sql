BEGIN;
LOCK TABLE setup_state IN ACCESS EXCLUSIVE MODE;
-- Schema 70 has no setup gate. Dropping an unfinished wizard would let the
-- older binary serve a half-initialised instance, so that blocks the
-- downgrade; finishing or deleting the wizard row first is an explicit choice.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM setup_state WHERE completed_at IS NULL) THEN
  RAISE EXCEPTION 'unfinished setup prevents downgrade' USING ERRCODE='55000';
 END IF;
END $$;
DROP TABLE setup_state;
DROP FUNCTION setup_state_final();
COMMIT;
