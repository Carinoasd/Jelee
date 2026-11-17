BEGIN;
-- G13.3: job total work and ETA. The total is only stored once it is known:
-- at submission for kinds whose work list is fixed or countable then, and for
-- an inventory scan only when enumeration has completed. Throughput is an
-- exponential moving average of done units per second, sampled from the
-- progress the existing fenced checkpoints already write, so no checkpoint
-- issues an extra statement. The mark is the last sample point; it is reset
-- whenever the job is not running, a new lease generation claims it or the
-- done count moves backwards (scan recovery), so pauses and queue time never
-- enter a sample.
ALTER TABLE jobs
 ADD COLUMN progress_total bigint CHECK(progress_total>=0),
 ADD COLUMN progress_total_at timestamptz,
 ADD COLUMN rate_ema double precision CHECK(rate_ema>=0 AND rate_ema<'Infinity'::double precision),
 ADD COLUMN rate_samples integer NOT NULL DEFAULT 0 CHECK(rate_samples BETWEEN 0 AND 1000000),
 ADD COLUMN rate_mark_at timestamptz,
 ADD COLUMN rate_mark_done bigint CHECK(rate_mark_done>=0),
 ADD CONSTRAINT jobs_progress_total_pair CHECK((progress_total IS NULL)=(progress_total_at IS NULL)),
 ADD CONSTRAINT jobs_rate_samples_pair CHECK((rate_ema IS NULL)=(rate_samples=0)),
 ADD CONSTRAINT jobs_rate_mark_pair CHECK((rate_mark_at IS NULL)=(rate_mark_done IS NULL));

-- The done unit mirrors domain.JobProgressDone: directories for an inventory
-- scan, files plus skipped for every other kind.
CREATE FUNCTION jobs_progress_rate() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE done bigint; at timestamptz := clock_timestamp(); elapsed double precision; sample double precision;
BEGIN
 IF NEW.kind='inventory_scan' THEN
  done := NEW.directories;
  IF NEW.directory_total>0 AND NEW.directories>=NEW.directory_total THEN
   IF NEW.progress_total IS DISTINCT FROM NEW.directory_total THEN
    NEW.progress_total := NEW.directory_total;
    NEW.progress_total_at := at;
   END IF;
  ELSE
   NEW.progress_total := NULL;
   NEW.progress_total_at := NULL;
  END IF;
 ELSE
  done := NEW.files+NEW.skipped;
 END IF;
 IF NEW.state<>'running' THEN
  NEW.rate_mark_at := NULL;
  NEW.rate_mark_done := NULL;
  RETURN NEW;
 END IF;
 IF OLD.state<>'running' OR NEW.generation<>OLD.generation OR NEW.rate_mark_at IS NULL OR done<NEW.rate_mark_done THEN
  NEW.rate_mark_at := at;
  NEW.rate_mark_done := done;
  RETURN NEW;
 END IF;
 elapsed := extract(epoch FROM at-NEW.rate_mark_at);
 -- Progress within 50 ms of the mark accumulates into the next sample.
 IF done=NEW.rate_mark_done OR elapsed<0.05 THEN
  RETURN NEW;
 END IF;
 sample := (done-NEW.rate_mark_done)/elapsed;
 NEW.rate_ema := CASE WHEN NEW.rate_samples=0 THEN sample ELSE 0.3*sample+0.7*NEW.rate_ema END;
 NEW.rate_samples := LEAST(NEW.rate_samples+1,1000000);
 NEW.rate_mark_at := at;
 NEW.rate_mark_done := done;
 RETURN NEW;
END $$;
CREATE TRIGGER jobs_progress_rate BEFORE UPDATE ON jobs FOR EACH ROW EXECUTE FUNCTION jobs_progress_rate();
COMMIT;
