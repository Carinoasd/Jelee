package telemetry

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// LogCounters reports what the log router lost since the process started
// (G46.8): records a full or closed sink queue dropped, and records a sink
// refused. It must only read memory.
type LogCounters interface {
	Dropped() uint64
	WriteFailures() uint64
}

// LogForwardStat is what one external log destination lost or shipped since
// the process started.
type LogForwardStat struct {
	Destination string
	Dropped     uint64
	Failures    uint64
	Sent        uint64
}

// LogForwardCounters is implemented by a LogCounters source that also feeds
// external destinations (syslog, Loki); RegisterLogging then exports the
// per-destination counters. It must only read memory.
type LogForwardCounters interface {
	ForwardStats() []LogForwardStat
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
	instruments := []metric.Observable{dropped, failed}
	var fwdDropped, fwdFailures, fwdSent metric.Int64ObservableCounter
	forward, forwarding := source.(LogForwardCounters)
	if forwarding {
		if fwdDropped, err = meter.Int64ObservableCounter("jelee.logging.forward.dropped", metric.WithDescription("Log records a forwarder destination lost (queue full, oversized, or delivery gave up) since process start (G46.8).")); err != nil {
			return err
		}
		if fwdFailures, err = meter.Int64ObservableCounter("jelee.logging.forward.failures", metric.WithDescription("Failed delivery attempts to a log forwarder destination since process start.")); err != nil {
			return err
		}
		if fwdSent, err = meter.Int64ObservableCounter("jelee.logging.forward.sent", metric.WithDescription("Log records a forwarder destination accepted since process start.")); err != nil {
			return err
		}
		instruments = append(instruments, fwdDropped, fwdFailures, fwdSent)
	}
	_, err = meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if m.stopped.Load() {
			return nil
		}
		observer.ObserveInt64(dropped, counterValue(source.Dropped()))
		observer.ObserveInt64(failed, counterValue(source.WriteFailures()))
		if forwarding {
			for _, st := range forward.ForwardStats() {
				labels := metric.WithAttributeSet(attribute.NewSet(attribute.String("destination", st.Destination)))
				observer.ObserveInt64(fwdDropped, counterValue(st.Dropped), labels)
				observer.ObserveInt64(fwdFailures, counterValue(st.Failures), labels)
				observer.ObserveInt64(fwdSent, counterValue(st.Sent), labels)
			}
		}
		return nil
	}, instruments...)
	return err
}

// counterValue clamps a uint64 counter into the int64 an instrument takes.
func counterValue(n uint64) int64 {
	if n > 1<<63-1 {
		return 1<<63 - 1
	}
	return int64(n)
}
