BEGIN;
-- Totals and throughput are estimates derived from progress the jobs keep;
-- dropping them loses no job state, only the ETA of jobs in flight.
DROP TRIGGER jobs_progress_rate ON jobs;
DROP FUNCTION jobs_progress_rate();
ALTER TABLE jobs
 DROP CONSTRAINT jobs_progress_total_pair,
 DROP CONSTRAINT jobs_rate_samples_pair,
 DROP CONSTRAINT jobs_rate_mark_pair,
 DROP COLUMN progress_total,
 DROP COLUMN progress_total_at,
 DROP COLUMN rate_ema,
 DROP COLUMN rate_samples,
 DROP COLUMN rate_mark_at,
 DROP COLUMN rate_mark_done;
COMMIT;
