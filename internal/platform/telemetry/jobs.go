package telemetry

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync/atomic"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

var errJobMetricsUnavailable = errors.New("metrics unavailable")

type jobDimensions struct {
	kind, priority string
}

func fixedJobDimensions() [8]jobDimensions {
	return [8]jobDimensions{
		{"catalog_import", "background"},
		{"catalog_import", "manual"},
		{"catalog_sync", "background"},
		{"catalog_sync", "manual"},
		{"inventory_scan", "background"},
		{"inventory_scan", "manual"},
		{"nfo_write", "background"},
		{"nfo_write", "manual"},
	}
}

func fixedJobOutcomes() [3]string { return [3]string{"succeeded", "failed", "cancelled"} }

// The snapshot is a value containing only fixed arrays. It stays immutable
// after publication; the receipt fields describe this one gather separately.
type jobMetricCollection struct {
	snapshot       app.JobMetricsSnapshot
	requestContext context.Context
	gatherStarted  atomic.Bool
	produced       atomic.Uint32
}

// This producer belongs only to the private HTTP exporter's reader. Additional
// SDK readers must use their own producer and collection receipt.
type jobMetricsProducer struct {
	active atomic.Pointer[jobMetricCollection]
}

var _ sdkmetric.Producer = (*jobMetricsProducer)(nil)

func (p *jobMetricsProducer) Produce(ctx context.Context) ([]metricdata.ScopeMetrics, error) {
	c := p.active.Load()
	if c == nil || !c.gatherStarted.Load() || ctx.Err() != nil || c.requestContext.Err() != nil {
		return nil, errJobMetricsUnavailable
	}
	// Rebuild every slice on each call, including histogram bounds and counts.
	// A reader can retain or mutate its result without changing the next one.
	result := jobScopeMetrics(c.snapshot)
	if p.active.Load() != c || ctx.Err() != nil || c.requestContext.Err() != nil {
		return nil, errJobMetricsUnavailable
	}
	c.produced.Add(1)
	return []metricdata.ScopeMetrics{result}, nil
}

func validJobSnapshot(s app.JobMetricsSnapshot) bool {
	if s.StartedAt.IsZero() || s.ObservedAt.IsZero() {
		return false
	}
	for i, d := range fixedJobDimensions() {
		g := s.Groups[i]
		if g.Kind != d.kind || g.Priority != d.priority ||
			g.Queued < 0 || g.Running < 0 || g.ExpiredRunning < 0 ||
			g.Succeeded < 0 || g.Failed < 0 || g.Cancelled < 0 ||
			!finiteNonnegative(g.OldestQueuedAgeSeconds) ||
			(g.Queued == 0 && g.OldestQueuedAgeSeconds != 0) ||
			!validJobHistogram(g.Wait) || !validJobHistogram(g.Duration) {
			return false
		}
		// Never-started cancellations have no duration sample. Subtracting
		// avoids overflowing when three large outcome counts are added.
		remaining := g.Duration.Count
		for _, n := range [3]int64{g.Succeeded, g.Failed, g.Cancelled} {
			if uint64(n) >= remaining {
				remaining = 0
				break
			}
			remaining -= uint64(n)
		}
		if remaining != 0 {
			return false
		}
	}
	return true
}

func finiteNonnegative(n float64) bool {
	return n >= 0 && !math.IsNaN(n) && !math.IsInf(n, 0)
}

func validJobHistogram(h app.JobMetricHistogram) bool {
	// The database stores bigint counts and numeric(30,0) microsecond sums.
	// Converting the largest stored sum to seconds can round up to 1e24.
	if h.Count > math.MaxInt64 || !finiteNonnegative(h.SumSeconds) ||
		h.SumSeconds > 1e24 || (h.Count == 0 && h.SumSeconds != 0) {
		return false
	}
	remaining := h.Count
	for _, n := range h.BucketCounts {
		if n > remaining {
			return false
		}
		remaining -= n
	}
	return remaining == 0
}

func jobScopeMetrics(s app.JobMetricsSnapshot) metricdata.ScopeMetrics {
	queued := make([]metricdata.DataPoint[int64], 0, len(s.Groups))
	running := make([]metricdata.DataPoint[int64], 0, len(s.Groups))
	expired := make([]metricdata.DataPoint[int64], 0, len(s.Groups))
	oldest := make([]metricdata.DataPoint[float64], 0, len(s.Groups))
	outcomes := make([]metricdata.DataPoint[int64], 0, 3*len(s.Groups))
	wait := make([]metricdata.HistogramDataPoint[float64], 0, len(s.Groups))
	duration := make([]metricdata.HistogramDataPoint[float64], 0, len(s.Groups))
	for i, d := range fixedJobDimensions() {
		g := s.Groups[i]
		attrs := attribute.NewSet(attribute.String("kind", d.kind), attribute.String("priority", d.priority))
		point := metricdata.DataPoint[int64]{Attributes: attrs, Time: s.ObservedAt, Value: g.Queued}
		queued = append(queued, point)
		point.Value = g.Running
		running = append(running, point)
		point.Value = g.ExpiredRunning
		expired = append(expired, point)
		oldest = append(oldest, metricdata.DataPoint[float64]{
			Attributes: attrs, Time: s.ObservedAt, Value: g.OldestQueuedAgeSeconds,
		})
		values := [3]int64{g.Succeeded, g.Failed, g.Cancelled}
		for j, outcome := range fixedJobOutcomes() {
			outcomes = append(outcomes, metricdata.DataPoint[int64]{
				Attributes: attribute.NewSet(attribute.String("kind", d.kind), attribute.String("priority", d.priority), attribute.String("outcome", outcome)),
				StartTime:  s.StartedAt, Time: s.ObservedAt, Value: values[j],
			})
		}
		wait = append(wait, jobHistogramPoint(s, attrs, g.Wait, app.JobWaitBoundsSeconds()))
		duration = append(duration, jobHistogramPoint(s, attrs, g.Duration, app.JobDurationBoundsSeconds()))
	}
	return metricdata.ScopeMetrics{
		Scope: instrumentation.Scope{Name: "github.com/MoYuanCN/Jelee/internal/platform/telemetry/jobs"},
		Metrics: []metricdata.Metrics{
			{Name: "jelee.jobs.shared.queued", Description: "Queued jobs in the shared database.", Data: metricdata.Gauge[int64]{DataPoints: queued}},
			{Name: "jelee.jobs.shared.running", Description: "Running jobs with a live lease in the shared database.", Data: metricdata.Gauge[int64]{DataPoints: running}},
			{Name: "jelee.jobs.shared.expired_running", Description: "Running jobs with an expired lease in the shared database.", Data: metricdata.Gauge[int64]{DataPoints: expired}},
			{Name: "jelee.jobs.shared.oldest_queued_age", Description: "Age of the oldest queued job in the shared database.", Unit: "s", Data: metricdata.Gauge[float64]{DataPoints: oldest}},
			{Name: "jelee.jobs.shared.outcomes", Description: "Job outcomes recorded since the shared metrics epoch.", Data: metricdata.Sum[int64]{DataPoints: outcomes, Temporality: metricdata.CumulativeTemporality, IsMonotonic: true}},
			{Name: "jelee.jobs.shared.initial_wait", Description: "Initial submission to first start since the shared metrics epoch.", Unit: "s", Data: metricdata.Histogram[float64]{DataPoints: wait, Temporality: metricdata.CumulativeTemporality}},
			{Name: "jelee.jobs.shared.duration", Description: "First start to finish since the shared metrics epoch, including retries.", Unit: "s", Data: metricdata.Histogram[float64]{DataPoints: duration, Temporality: metricdata.CumulativeTemporality}},
		},
	}
}

func jobHistogramPoint(s app.JobMetricsSnapshot, attrs attribute.Set, h app.JobMetricHistogram, bounds [12]float64) metricdata.HistogramDataPoint[float64] {
	return metricdata.HistogramDataPoint[float64]{
		Attributes: attrs, StartTime: s.StartedAt, Time: s.ObservedAt,
		Count: h.Count, Sum: h.SumSeconds,
		Bounds: append([]float64(nil), bounds[:]...), BucketCounts: append([]uint64(nil), h.BucketCounts[:]...),
	}
}

// The exporter can swallow an external Producer error and keep local metrics.
// Discard any partial gather before promhttp starts encoding its response.
type jobMetricsGatherer struct {
	registry prometheus.Gatherer
	producer *jobMetricsProducer
}

func (g jobMetricsGatherer) Gather() ([]*dto.MetricFamily, error) {
	c := g.producer.active.Load()
	if c == nil || c.requestContext.Err() != nil || !c.gatherStarted.CompareAndSwap(false, true) {
		return nil, errJobMetricsUnavailable
	}
	families, err := g.registry.Gather()
	if err != nil || g.producer.active.Load() != c || c.requestContext.Err() != nil ||
		c.produced.Load() != 1 || !completeJobFamilies(families) {
		return nil, errJobMetricsUnavailable
	}
	return families, nil
}

func completeJobFamilies(families []*dto.MetricFamily) bool {
	expected := [...]struct {
		name string
		kind dto.MetricType
	}{
		{"jelee_jobs_shared_queued", dto.MetricType_GAUGE},
		{"jelee_jobs_shared_running", dto.MetricType_GAUGE},
		{"jelee_jobs_shared_expired_running", dto.MetricType_GAUGE},
		{"jelee_jobs_shared_oldest_queued_age_seconds", dto.MetricType_GAUGE},
		{"jelee_jobs_shared_outcomes_total", dto.MetricType_COUNTER},
		{"jelee_jobs_shared_initial_wait_seconds", dto.MetricType_HISTOGRAM},
		{"jelee_jobs_shared_duration_seconds", dto.MetricType_HISTOGRAM},
	}
	var seen [len(expected)]bool
	for _, family := range families {
		name := family.GetName()
		if !strings.HasPrefix(name, "jelee_jobs_shared_") {
			continue
		}
		index := -1
		for i, spec := range expected {
			if name == spec.name {
				index = i
				break
			}
		}
		if index < 0 || seen[index] || family.GetType() != expected[index].kind {
			return false
		}
		seen[index] = true
		withOutcome := expected[index].kind == dto.MetricType_COUNTER
		groups := uint(len(fixedJobDimensions()))
		want := uint32(1)<<groups - 1
		if withOutcome {
			want = uint32(1)<<(groups*uint(len(fixedJobOutcomes()))) - 1
		}
		var points uint32
		for _, point := range family.Metric {
			key, ok := jobPointKey(point, withOutcome)
			if !ok || points&(1<<key) != 0 {
				return false
			}
			points |= 1 << key
		}
		if points != want {
			return false
		}
	}
	for _, found := range seen {
		if !found {
			return false
		}
	}
	return true
}

func jobPointKey(point *dto.Metric, withOutcome bool) (uint, bool) {
	want := 2
	if withOutcome {
		want = 3
	}
	if point == nil || len(point.Label) != want {
		return 0, false
	}
	var kind, priority, outcome string
	for _, label := range point.Label {
		switch label.GetName() {
		case "kind":
			if kind != "" {
				return 0, false
			}
			kind = label.GetValue()
		case "priority":
			if priority != "" {
				return 0, false
			}
			priority = label.GetValue()
		case "outcome":
			if !withOutcome || outcome != "" {
				return 0, false
			}
			outcome = label.GetValue()
		default:
			return 0, false
		}
	}
	for i, d := range fixedJobDimensions() {
		if kind != d.kind || priority != d.priority {
			continue
		}
		if !withOutcome {
			return uint(i), true
		}
		for j, name := range fixedJobOutcomes() {
			if outcome == name {
				return uint(i*3 + j), true
			}
		}
	}
	return 0, false
}
