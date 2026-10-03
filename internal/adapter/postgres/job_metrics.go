package postgres

import (
	"context"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
)

var _ app.JobMetricsSource = (*Store)(nil)

// One statement gives the active queue and durable counters the same MVCC
// snapshot. The active predicate uses the existing partial jobs indexes; no
// inventory/history rows are joined, recovered, or locked for update.
const jobMetricsSQL = `WITH observed AS MATERIALIZED (
 SELECT statement_timestamp() AS at
), active AS (
 SELECT kind,priority,
  count(*) FILTER (WHERE state='queued') AS queued,
  count(*) FILTER (WHERE state='running' AND lease_until>(SELECT at FROM observed)) AS running,
  count(*) FILTER (WHERE state='running' AND lease_until<=(SELECT at FROM observed)) AS expired,
  min(created_at) FILTER (WHERE state='queued') AS oldest
 FROM jobs WHERE state IN ('queued','running') GROUP BY kind,priority
)
SELECT e.started_at,o.at,t.kind,t.priority,
 COALESCE(a.queued,0),COALESCE(a.running,0),COALESCE(a.expired,0),
 COALESCE(GREATEST(extract(epoch FROM o.at-a.oldest),0),0)::double precision,
 t.succeeded_total,t.failed_total,t.cancelled_total,
 t.wait_count,t.wait_sum_microseconds::text,t.duration_count,t.duration_sum_microseconds::text,
 b.wait_buckets,b.duration_buckets
FROM job_metric_epoch e CROSS JOIN observed o CROSS JOIN job_metric_totals t
LEFT JOIN active a USING(kind,priority)
CROSS JOIN LATERAL (
 SELECT array_agg(bucket_count ORDER BY bucket_index) FILTER(WHERE measure='wait') AS wait_buckets,
  array_agg(bucket_count ORDER BY bucket_index) FILTER(WHERE measure='duration') AS duration_buckets
 FROM job_metric_buckets WHERE kind=t.kind AND priority=t.priority
) b
WHERE e.singleton ORDER BY t.kind,t.priority`

// JobMetrics reads database-wide totals without the writer advisory lock. The
// deadline also bounds connection acquisition. Never expose a partial snapshot
// or a driver error containing connection details to a telemetry caller.
func (s *Store) JobMetrics(ctx context.Context) (app.JobMetricsSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	failure := func() (app.JobMetricsSnapshot, error) {
		if err := ctx.Err(); err != nil {
			return app.JobMetricsSnapshot{}, err
		}
		return app.JobMetricsSnapshot{}, errors.New("job metrics unavailable")
	}
	rows, err := s.Pool.Query(ctx, jobMetricsSQL)
	if err != nil {
		return failure()
	}
	defer rows.Close()
	var result app.JobMetricsSnapshot
	expected := [6][2]string{{"catalog_import", "background"}, {"catalog_import", "manual"}, {"inventory_scan", "background"}, {"inventory_scan", "manual"}, {"nfo_write", "background"}, {"nfo_write", "manual"}}
	index := 0
	for rows.Next() {
		if index >= len(result.Groups) {
			return failure()
		}
		g := &result.Groups[index]
		var started, observed time.Time
		var waitCount, durationCount int64
		var waitSum, durationSum string
		var waitBuckets, durationBuckets []int64
		err = rows.Scan(&started, &observed, &g.Kind, &g.Priority,
			&g.Queued, &g.Running, &g.ExpiredRunning, &g.OldestQueuedAgeSeconds,
			&g.Succeeded, &g.Failed, &g.Cancelled, &waitCount, &waitSum, &durationCount, &durationSum,
			&waitBuckets, &durationBuckets)
		if err != nil || started.IsZero() || observed.IsZero() ||
			g.Kind != expected[index][0] || g.Priority != expected[index][1] ||
			g.Queued < 0 || g.Running < 0 || g.ExpiredRunning < 0 ||
			g.Succeeded < 0 || g.Failed < 0 || g.Cancelled < 0 ||
			!finiteNonnegative(g.OldestQueuedAgeSeconds) {
			return failure()
		}
		if index == 0 {
			result.StartedAt, result.ObservedAt = started, observed
		} else if !started.Equal(result.StartedAt) || !observed.Equal(result.ObservedAt) {
			return failure()
		}
		var valid bool
		if g.Wait, valid = jobMetricHistogram(waitCount, waitSum, waitBuckets); !valid {
			return failure()
		}
		if g.Duration, valid = jobMetricHistogram(durationCount, durationSum, durationBuckets); !valid {
			return failure()
		}
		index++
	}
	if rows.Err() != nil || index != len(result.Groups) || ctx.Err() != nil {
		return failure()
	}
	return result, nil
}

func jobMetricHistogram(count int64, sum string, buckets []int64) (app.JobMetricHistogram, bool) {
	var h app.JobMetricHistogram
	if count < 0 || len(buckets) != len(h.BucketCounts) || len(sum) == 0 || len(sum) > 30 {
		return h, false
	}
	// numeric(30,0) is kept exact in storage. Only finite nonnegative integer
	// microseconds cross this boundary, then Prometheus seconds use float64.
	for _, c := range sum {
		if c < '0' || c > '9' {
			return h, false
		}
	}
	value, err := strconv.ParseFloat(sum, 64)
	if err != nil || !finiteNonnegative(value) {
		return h, false
	}
	h.Count, h.SumSeconds = uint64(count), value/1e6
	var total uint64
	for i, n := range buckets {
		if n < 0 || uint64(n) > h.Count-total {
			return app.JobMetricHistogram{}, false
		}
		h.BucketCounts[i] = uint64(n)
		total += uint64(n)
	}
	if total != h.Count || (h.Count == 0 && value != 0) {
		return app.JobMetricHistogram{}, false
	}
	return h, true
}

func finiteNonnegative(value float64) bool {
	return value >= 0 && !math.IsInf(value, 0) && !math.IsNaN(value)
}
