BEGIN;
-- No state transition may commit between establishing the epoch and installing
-- the trigger. Existing retained history is not a complete historical counter.
LOCK TABLE jobs IN SHARE ROW EXCLUSIVE MODE;

CREATE TABLE job_metric_epoch (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 started_at timestamptz NOT NULL DEFAULT clock_timestamp() CHECK(isfinite(started_at))
);
INSERT INTO job_metric_epoch(singleton) VALUES(true);

CREATE TABLE job_metric_totals (
 kind text NOT NULL CHECK(kind IN ('inventory_scan','catalog_import')),
 priority text NOT NULL CHECK(priority IN ('manual','background')),
 succeeded_total bigint NOT NULL DEFAULT 0 CHECK(succeeded_total>=0),
 failed_total bigint NOT NULL DEFAULT 0 CHECK(failed_total>=0),
 cancelled_total bigint NOT NULL DEFAULT 0 CHECK(cancelled_total>=0),
 wait_count bigint NOT NULL DEFAULT 0 CHECK(wait_count>=0),
 wait_sum_microseconds numeric(30,0) NOT NULL DEFAULT 0
  CHECK(wait_sum_microseconds BETWEEN 0 AND 999999999999999999999999999999),
 duration_count bigint NOT NULL DEFAULT 0 CHECK(duration_count>=0),
 duration_sum_microseconds numeric(30,0) NOT NULL DEFAULT 0
  CHECK(duration_sum_microseconds BETWEEN 0 AND 999999999999999999999999999999),
 CHECK(wait_count>0 OR wait_sum_microseconds=0),
 CHECK(duration_count>0 OR duration_sum_microseconds=0),
 CHECK(duration_count::numeric<=succeeded_total::numeric+failed_total::numeric+cancelled_total::numeric),
 PRIMARY KEY(kind,priority)
);
INSERT INTO job_metric_totals(kind,priority)
 SELECT kind,priority
 FROM (VALUES('inventory_scan'),('catalog_import')) AS kinds(kind)
 CROSS JOIN (VALUES('manual'),('background')) AS priorities(priority);

-- Each observation increments exactly one noncumulative bucket. Index 12 is
-- the overflow bucket; exporters prefix-sum when rendering cumulative buckets.
CREATE TABLE job_metric_buckets (
 kind text NOT NULL,
 priority text NOT NULL,
 measure text NOT NULL CHECK(measure IN ('wait','duration')),
 bucket_index smallint NOT NULL CHECK(bucket_index BETWEEN 0 AND 12),
 upper_bound_microseconds bigint,
 bucket_count bigint NOT NULL DEFAULT 0 CHECK(bucket_count>=0),
 CHECK(
  (bucket_index=12 AND upper_bound_microseconds IS NULL)
  OR (bucket_index<12 AND upper_bound_microseconds IS NOT NULL
   AND upper_bound_microseconds=(CASE WHEN measure='wait' THEN
    ARRAY[100000,500000,1000000,2500000,5000000,10000000,30000000,60000000,300000000,1800000000,3600000000,86400000000]::bigint[]
   ELSE
    ARRAY[1000000,5000000,10000000,30000000,60000000,300000000,900000000,1800000000,3600000000,7200000000,21600000000,86400000000]::bigint[]
   END)[bucket_index+1])
 ),
 PRIMARY KEY(kind,priority,measure,bucket_index),
 FOREIGN KEY(kind,priority) REFERENCES job_metric_totals(kind,priority)
);
INSERT INTO job_metric_buckets(kind,priority,measure,bucket_index,upper_bound_microseconds)
 SELECT kind,priority,measure,bucket_index,
  (CASE WHEN measure='wait' THEN
   ARRAY[100000,500000,1000000,2500000,5000000,10000000,30000000,60000000,300000000,1800000000,3600000000,86400000000]::bigint[]
  ELSE
   ARRAY[1000000,5000000,10000000,30000000,60000000,300000000,900000000,1800000000,3600000000,7200000000,21600000000,86400000000]::bigint[]
  END)[bucket_index+1]
 FROM job_metric_totals
 CROSS JOIN (VALUES('wait'),('duration')) AS measures(measure)
 CROSS JOIN generate_series(0,12) AS indices(bucket_index);

CREATE FUNCTION record_job_metrics() RETURNS trigger
 LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog AS $$
DECLARE
 metric_measure text;
 elapsed_microseconds numeric;
 bounds bigint[];
 selected_bucket integer:=12;
 index integer;
 affected bigint;
 has_duration boolean:=false;
BEGIN
 -- Reclaims preserve started_at. Terminal rows are never reopened by the
 -- product; retry creates a different job with a new initial wait sample.
 IF OLD.state='queued' AND NEW.state='running'
  AND OLD.started_at IS NULL AND NEW.started_at IS NOT NULL THEN
  IF NOT isfinite(NEW.created_at) OR NOT isfinite(NEW.started_at) THEN
   RAISE EXCEPTION 'invalid job metric timestamp' USING ERRCODE='23514';
  END IF;
  metric_measure:='wait';
  elapsed_microseconds:=greatest(0,extract(epoch FROM (NEW.started_at-NEW.created_at))*1000000);
  bounds:=ARRAY[100000,500000,1000000,2500000,5000000,10000000,30000000,60000000,300000000,1800000000,3600000000,86400000000]::bigint[];
 ELSIF OLD.state IN ('queued','running') AND NEW.state IN ('succeeded','failed','cancelled') THEN
  IF NEW.finished_at IS NULL OR NOT isfinite(NEW.finished_at)
   OR (NEW.started_at IS NOT NULL AND NOT isfinite(NEW.started_at)) THEN
   RAISE EXCEPTION 'invalid job metric timestamp' USING ERRCODE='23514';
  END IF;
  has_duration:=NEW.started_at IS NOT NULL;
  elapsed_microseconds:=0;
  IF has_duration THEN
   metric_measure:='duration';
   elapsed_microseconds:=greatest(0,extract(epoch FROM (NEW.finished_at-NEW.started_at))*1000000);
   bounds:=ARRAY[1000000,5000000,10000000,30000000,60000000,300000000,900000000,1800000000,3600000000,7200000000,21600000000,86400000000]::bigint[];
  END IF;
 ELSE
  RETURN NEW;
 END IF;

 -- All object names come from the table that owns this trigger. A caller's
 -- search_path cannot redirect counters into another schema or temp table.
 EXECUTE format('SELECT count(*) FROM %I.job_metric_epoch WHERE singleton',TG_TABLE_SCHEMA) INTO affected;
 IF affected<>1 THEN
  RAISE EXCEPTION 'job metric epoch is unavailable' USING ERRCODE='23514';
 END IF;
 IF elapsed_microseconds NOT BETWEEN 0 AND 999999999999999999999999999999 THEN
  RAISE EXCEPTION 'job metric duration exceeds supported range' USING ERRCODE='23514';
 END IF;

 IF metric_measure='wait' THEN
  EXECUTE format('UPDATE %I.job_metric_totals SET wait_count=wait_count+1,wait_sum_microseconds=wait_sum_microseconds+$3 WHERE kind=$1 AND priority=$2',TG_TABLE_SCHEMA)
   USING NEW.kind,NEW.priority,elapsed_microseconds;
 ELSE
  EXECUTE format('UPDATE %I.job_metric_totals SET succeeded_total=succeeded_total+CASE WHEN $3=''succeeded'' THEN 1 ELSE 0 END,failed_total=failed_total+CASE WHEN $3=''failed'' THEN 1 ELSE 0 END,cancelled_total=cancelled_total+CASE WHEN $3=''cancelled'' THEN 1 ELSE 0 END,duration_count=duration_count+CASE WHEN $4 THEN 1 ELSE 0 END,duration_sum_microseconds=duration_sum_microseconds+$5 WHERE kind=$1 AND priority=$2',TG_TABLE_SCHEMA)
   USING NEW.kind,NEW.priority,NEW.state,has_duration,elapsed_microseconds;
 END IF;
 GET DIAGNOSTICS affected=ROW_COUNT;
 IF affected<>1 THEN
  RAISE EXCEPTION 'job metric totals are unavailable' USING ERRCODE='23514';
 END IF;

 IF metric_measure IS NOT NULL THEN
  -- Derive the expected bucket from fixed bounds, not existing rows. Missing
  -- buckets must roll back the transition rather than silently shift a sample.
  FOR index IN 1..12 LOOP
   IF elapsed_microseconds<=bounds[index] THEN
    selected_bucket:=index-1;
    EXIT;
   END IF;
  END LOOP;
  EXECUTE format('UPDATE %I.job_metric_buckets SET bucket_count=bucket_count+1 WHERE kind=$1 AND priority=$2 AND measure=$3 AND bucket_index=$4',TG_TABLE_SCHEMA)
   USING NEW.kind,NEW.priority,metric_measure,selected_bucket;
  GET DIAGNOSTICS affected=ROW_COUNT;
  IF affected<>1 THEN
   RAISE EXCEPTION 'job metric bucket is unavailable' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER jobs_record_metrics AFTER UPDATE OF state,started_at ON jobs
 FOR EACH ROW WHEN (OLD.state IS DISTINCT FROM NEW.state)
 EXECUTE FUNCTION record_job_metrics();
COMMIT;
