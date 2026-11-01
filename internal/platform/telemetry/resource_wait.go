package telemetry

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/resources"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// Shared budget wait distribution (G41.8): jelee_resources_wait_seconds, a
// histogram by work class (cpu, io) and outcome (granted, cancelled) of how
// long admissions to the shared resource budget waited, 1 ms to 60 s. The
// budget keeps the counts under its own lock; collection copies them once
// and never acquires a permit. Like the other resource metrics the values
// are per process, so replicas add up.

// resourceWaitProducer exposes the registered budget; before registration
// it produces nothing.
type resourceWaitProducer struct {
	budget atomic.Pointer[resources.Budget]
}

var _ sdkmetric.Producer = (*resourceWaitProducer)(nil)

func (p *resourceWaitProducer) Produce(ctx context.Context) ([]metricdata.ScopeMetrics, error) {
	budget := p.budget.Load()
	if budget == nil {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	series, started := budget.WaitStats()
	return []metricdata.ScopeMetrics{resourceWaitScopeMetrics(series, started, time.Now())}, nil
}

func resourceWaitScopeMetrics(series []resources.WaitSeries, started, now time.Time) metricdata.ScopeMetrics {
	if now.Before(started) {
		now = started
	}
	bounds := resources.WaitBoundsSeconds()
	points := make([]metricdata.HistogramDataPoint[float64], 0, len(series))
	for _, s := range series {
		points = append(points, metricdata.HistogramDataPoint[float64]{
			Attributes: attribute.NewSet(attribute.String("class", s.Class), attribute.String("outcome", s.Outcome)),
			StartTime:  started, Time: now, Count: s.Histogram.Count, Sum: s.Histogram.SumSeconds,
			Bounds: append([]float64(nil), bounds[:]...), BucketCounts: append([]uint64(nil), s.Histogram.BucketCounts[:]...),
		})
	}
	return metricdata.ScopeMetrics{
		Scope: instrumentation.Scope{Name: "github.com/MoYuanCN/Jelee/internal/platform/telemetry/resources"},
		Metrics: []metricdata.Metrics{{
			Name:        "jelee.resources.wait",
			Description: "Time admissions to the shared resource budget waited, by work class and outcome; immediate grants count as zero.",
			Unit:        "s",
			Data:        metricdata.Histogram[float64]{DataPoints: points, Temporality: metricdata.CumulativeTemporality},
		}},
	}
}
