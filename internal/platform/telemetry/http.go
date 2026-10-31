package telemetry

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/metric"
)

// RequestGuardCounters reports what the HTTP request guards refused since
// the process started (G08.7). It must only read memory.
type RequestGuardCounters interface {
	RateLimitedTotal() int64
	TimeoutsTotal() int64
}

// RegisterRequestGuards exports the general API rate limit refusals and the
// requests answered with a processing timeout.
func (m *Metrics) RegisterRequestGuards(source RequestGuardCounters) error {
	if source == nil {
		return errors.New("telemetry requires a request guard source")
	}
	meter := m.provider.Meter(telemetryMeter)
	limited, err := meter.Int64ObservableCounter("jelee.http.rate_limited", metric.WithDescription("API requests refused with 429 rate_limited by the per-client rate limit since process start (per instance)."))
	if err != nil {
		return err
	}
	timedOut, err := meter.Int64ObservableCounter("jelee.http.timeouts", metric.WithDescription("API requests answered 504 processing_timeout since process start."))
	if err != nil {
		return err
	}
	_, err = meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !m.stopped.Load() {
			observer.ObserveInt64(limited, max(0, source.RateLimitedTotal()))
			observer.ObserveInt64(timedOut, max(0, source.TimeoutsTotal()))
		}
		return nil
	}, limited, timedOut)
	return err
}
