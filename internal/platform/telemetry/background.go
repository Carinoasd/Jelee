package telemetry

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// Background job metrics (G41.1, G41.8): the recurring jobs of the job
// executor's background lane on this instance, with the kind, priority and
// outcome dimensions of the shared job metrics. Unlike jelee_jobs_shared_*,
// the values are per process and start with it, so replicas add up.

type backgroundSourceHolder struct {
	source app.BackgroundJobMetricsSource
}

// backgroundProducer exposes the registered source; before registration it
// produces nothing.
type backgroundProducer struct {
	source atomic.Pointer[backgroundSourceHolder]
}

var _ sdkmetric.Producer = (*backgroundProducer)(nil)

var errBackgroundMetricsInvalid = errors.New("background job metrics invalid")

func (p *backgroundProducer) Produce(ctx context.Context) ([]metricdata.ScopeMetrics, error) {
	holder := p.source.Load()
	if holder == nil {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snapshot := holder.source.BackgroundJobMetrics()
	if !validBackgroundSnapshot(snapshot) {
		return nil, errBackgroundMetricsInvalid
	}
	return []metricdata.ScopeMetrics{backgroundScopeMetrics(snapshot)}, nil
}

// RegisterBackgroundJobs exports the background job metrics of source. Call
// it once, before the handler serves.
func (m *Metrics) RegisterBackgroundJobs(source app.BackgroundJobMetricsSource) error {
	if source == nil {
		return errors.New("telemetry requires a background job source")
	}
	if !m.background.source.CompareAndSwap(nil, &backgroundSourceHolder{source: source}) {
		return errors.New("background job metrics already registered")
	}
	return nil
}

func validBackgroundSnapshot(s app.BackgroundJobMetricsSnapshot) bool {
	if s.StartedAt.IsZero() {
		return false
	}
	for i, kind := range domain.BackgroundJobKinds() {
		g := s.Groups[i]
		if g.Kind != kind || g.Running < 0 || g.Running > 1 || !validJobHistogram(g.Wait) || !validJobHistogram(g.Duration) {
			return false
		}
		// Every started run has a wait sample and ends with an outcome and a
		// duration sample; skipped runs and runs cancelled before they
		// started have only an outcome.
		if g.Wait.Count != g.Duration.Count+uint64(g.Running) {
			return false
		}
		var outcomes uint64
		for _, n := range g.Outcomes {
			outcomes += n
		}
		if outcomes < g.Duration.Count {
			return false
		}
	}
	return true
}

func backgroundScopeMetrics(s app.BackgroundJobMetricsSnapshot) metricdata.ScopeMetrics {
	now := s.StartedAt
	if t := time.Now(); t.After(now) {
		now = t
	}
	groups := len(s.Groups)
	running := make([]metricdata.DataPoint[int64], 0, groups)
	runs := make([]metricdata.DataPoint[int64], 0, groups*len(app.BackgroundJobGroup{}.Outcomes))
	wait := make([]metricdata.HistogramDataPoint[float64], 0, groups)
	duration := make([]metricdata.HistogramDataPoint[float64], 0, groups)
	bounds := app.BackgroundJobBoundsSeconds()
	for _, g := range s.Groups {
		attrs := attribute.NewSet(attribute.String("kind", g.Kind), attribute.String("priority", domain.BackgroundJobPriority))
		running = append(running, metricdata.DataPoint[int64]{Attributes: attrs, Time: now, Value: g.Running})
		for j, outcome := range domain.BackgroundRunOutcomes() {
			runs = append(runs, metricdata.DataPoint[int64]{
				Attributes: attribute.NewSet(attribute.String("kind", g.Kind), attribute.String("priority", domain.BackgroundJobPriority), attribute.String("outcome", outcome)),
				StartTime:  s.StartedAt, Time: now, Value: int64(min(g.Outcomes[j], 1<<62)), //nolint:gosec // G115: clamped below MaxInt64
			})
		}
		for _, h := range []struct {
			data *app.JobMetricHistogram
			out  *[]metricdata.HistogramDataPoint[float64]
		}{{&g.Wait, &wait}, {&g.Duration, &duration}} {
			*h.out = append(*h.out, metricdata.HistogramDataPoint[float64]{
				Attributes: attrs, StartTime: s.StartedAt, Time: now, Count: h.data.Count, Sum: h.data.SumSeconds,
				Bounds: append([]float64(nil), bounds[:]...), BucketCounts: append([]uint64(nil), h.data.BucketCounts[:]...),
			})
		}
	}
	return metricdata.ScopeMetrics{
		Scope: instrumentation.Scope{Name: "github.com/MoYuanCN/Jelee/internal/platform/telemetry/background"},
		Metrics: []metricdata.Metrics{
			{Name: "jelee.jobs.background.running", Description: "Background job runs in progress on this instance.", Data: metricdata.Gauge[int64]{DataPoints: running}},
			{Name: "jelee.jobs.background.runs", Description: "Background job runs on this instance since start, by outcome; skipped runs found the job running on another instance or no room in the shared budget.", Data: metricdata.Sum[int64]{DataPoints: runs, Temporality: metricdata.CumulativeTemporality, IsMonotonic: true}},
			{Name: "jelee.jobs.background.wait", Description: "Time from a background job run becoming due to its start, after the cluster lock and the shared budget.", Unit: "s", Data: metricdata.Histogram[float64]{DataPoints: wait, Temporality: metricdata.CumulativeTemporality}},
			{Name: "jelee.jobs.background.duration", Description: "Duration of background job runs on this instance.", Unit: "s", Data: metricdata.Histogram[float64]{DataPoints: duration, Temporality: metricdata.CumulativeTemporality}},
		},
	}
}
