package app

import (
	"context"
	"time"
)

// JobMetricsSource reads a bounded, consistent database snapshot. It must not
// recover leases or take the job writer lock. Failures return no partial data.
type JobMetricsSource interface {
	JobMetrics(context.Context) (JobMetricsSnapshot, error)
}

// JobMetricsSnapshot is shared by every instance using the same database schema.
// Cumulative values include only transitions observed since StartedAt; retained
// job history is not backfilled. Groups are ordered by kind, then priority.
type JobMetricsSnapshot struct {
	StartedAt  time.Time
	ObservedAt time.Time
	Groups     [4]JobMetricGroup
}

type JobMetricGroup struct {
	Kind                   string
	Priority               string
	Queued                 int64
	Running                int64
	ExpiredRunning         int64
	OldestQueuedAgeSeconds float64
	Succeeded              int64
	Failed                 int64
	Cancelled              int64
	Wait                   JobMetricHistogram
	Duration               JobMetricHistogram
}

// JobMetricHistogram holds mutually exclusive buckets, including the last +Inf
// bucket. SumSeconds is converted from exact stored microseconds to float64;
// very large lifetime totals can lose subsecond precision at exposition time.
// Wait is initial submission to first start; Duration is first start to finish,
// including intervals between attempts. Never-started cancellations lack a
// duration sample. Repeated reads must not be replayed into Histogram.Record.
type JobMetricHistogram struct {
	Count        uint64
	SumSeconds   float64
	BucketCounts [13]uint64
}

// Returning arrays prevents callers from mutating the shared bucket contract.
func JobWaitBoundsSeconds() [12]float64 {
	return [12]float64{0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 300, 1800, 3600, 86400}
}

func JobDurationBoundsSeconds() [12]float64 {
	return [12]float64{1, 5, 10, 30, 60, 300, 900, 1800, 3600, 7200, 21600, 86400}
}
