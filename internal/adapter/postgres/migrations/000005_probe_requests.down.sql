BEGIN;
-- Dropping active requests would let an older worker skip opted-in probing.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM probe_requests r JOIN jobs j ON j.id=r.job_id WHERE j.state IN ('queued','running')) THEN
  RAISE EXCEPTION 'finish or cancel requested probe jobs before rollback';
 END IF;
END $$;
DROP TABLE probe_requests;
DROP FUNCTION probe_request_immutable();
COMMIT;
