package telemetry

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/platform/resources"
	"go.opentelemetry.io/otel/metric"
)

// NewWithResources adds process-local admission snapshots to the private
// exporter. Collection only reads the budget; it never acquires work permits.
func NewWithResources(pool app.PoolStatsSource, jobs app.JobMetricsSource, budget *resources.Budget) (*Metrics, error) {
	if budget == nil {
		return nil, errors.New("telemetry requires a resource budget")
	}
	m, err := NewWithJobs(pool, jobs)
	if err != nil {
		return nil, err
	}
	if err := m.registerResources(budget); err != nil {
		return nil, errors.Join(err, m.Shutdown(context.Background()))
	}
	return m, nil
}

func (m *Metrics) registerResources(budget *resources.Budget) error {
	limits := budget.Limits()
	meter := m.provider.Meter("github.com/MoYuanCN/Jelee/internal/platform/telemetry")
	names := []string{"cpu.active", "io.active", "total.active", "waiting", "cpu.limit", "io.limit", "total.limit", "queue.limit"}
	gauges := make([]metric.Int64ObservableGauge, len(names))
	instruments := make([]metric.Observable, len(names))
	for i, name := range names {
		gauge, err := meter.Int64ObservableGauge("jelee.resources." + name)
		if err != nil {
			return err
		}
		gauges[i], instruments[i] = gauge, gauge
	}
	_, err := meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if m.stopped.Load() {
			return nil
		}
		// One locked snapshot keeps CPU + I/O equal to total in each scrape.
		s := budget.Stats()
		values := []int{s.CPU, s.IO, s.Total, s.Waiting, limits.CPU, limits.IO, limits.Total, limits.Queue}
		for i, value := range values {
			observer.ObserveInt64(gauges[i], int64(value))
		}
		return nil
	}, instruments...)
	return err
}
