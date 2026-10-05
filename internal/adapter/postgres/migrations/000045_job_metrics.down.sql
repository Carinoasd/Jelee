BEGIN;
-- Freeze transitions and metric data before proving that the epoch is empty.
-- Retained totals cannot be reconstructed after trimJobs removes job history.
LOCK TABLE jobs,job_metric_epoch,job_metric_totals,job_metric_buckets IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF (SELECT count(*) FROM job_metric_epoch)<>1
 OR (SELECT count(*) FROM job_metric_totals)<>4
 OR (SELECT count(*) FROM job_metric_buckets)<>104 THEN
  RAISE EXCEPTION 'incomplete job metrics prevent downgrade';
 END IF;
 IF EXISTS(SELECT 1 FROM job_metric_totals
  WHERE succeeded_total<>0 OR failed_total<>0 OR cancelled_total<>0
   OR wait_count<>0 OR wait_sum_microseconds<>0
   OR duration_count<>0 OR duration_sum_microseconds<>0)
 OR EXISTS(SELECT 1 FROM job_metric_buckets WHERE bucket_count<>0) THEN
  RAISE EXCEPTION 'retained job metrics prevent downgrade';
 END IF;
END $$;
DROP TRIGGER jobs_record_metrics ON jobs;
DROP FUNCTION record_job_metrics();
DROP TABLE job_metric_buckets;
DROP TABLE job_metric_totals;
DROP TABLE job_metric_epoch;
COMMIT;
