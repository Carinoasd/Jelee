package telemetry

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/metric"
)

// LogCounters reports what the log router lost since the process started
// (G46.8): records a full or closed sink queue dropped, and records a sink
// refused. It must only read memory.
type LogCounters interface {
	Dropped() uint64
	WriteFailures() uint64
}

// RegisterLogging exports the log loss counters. The default alert
// JeleeLogRecordsDropped is a rate over the dropped counter.
func (m *Metrics) RegisterLogging(source LogCounters) error {
	if source == nil {
		return errors.New("telemetry requires a log counter source")
	}
	meter := m.provider.Meter(telemetryMeter)
	dropped, err := meter.Int64ObservableCounter("jelee.logging.records.dropped", metric.WithDescription("Log records dropped because a sink queue was full or closed since process start (G46.8 backpressure)."))
	if err != nil {
		return err
	}
	failed, err := meter.Int64ObservableCounter("jelee.logging.write_failures", metric.WithDescription("Log records a sink (file or forwarder) refused since process start."))
	if err != nil {
		return err
	}
	_, err = meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !m.stopped.Load() {
			observer.ObserveInt64(dropped, counterValue(source.Dropped()))
			observer.ObserveInt64(failed, counterValue(source.WriteFailures()))
		}
		return nil
	}, dropped, failed)
	return err
}

// counterValue clamps a uint64 counter into the int64 an instrument takes.
func counterValue(n uint64) int64 {
	if n > 1<<63-1 {
		return 1<<63 - 1
	}
	return int64(n)
}
