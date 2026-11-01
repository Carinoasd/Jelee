package telemetry

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// TraceExportCounters reports what the OTLP trace exporter delivered and
// lost since the process started (G46.6, G46.8). It must only read memory.
type TraceExportCounters interface {
	Exported() uint64
	ExportFailures() uint64
	// Dropped reports the spans lost for one of the reasons passed to
	// RegisterTraceExport.
	Dropped(reason string) uint64
}

// RegisterTraceExport exports the trace exporter counters. It is only
// registered when trace export is configured, so the families are absent
// while export is off.
func (m *Metrics) RegisterTraceExport(source TraceExportCounters, reasons []string) error {
	if source == nil || len(reasons) == 0 {
		return errors.New("telemetry requires a trace export counter source")
	}
	meter := m.provider.Meter(telemetryMeter)
	exported, err := meter.Int64ObservableCounter("jelee.tracing.spans.exported", metric.WithDescription("Spans the OTLP collector accepted since process start (G46.6)."))
	if err != nil {
		return err
	}
	dropped, err := meter.Int64ObservableCounter("jelee.tracing.spans.dropped", metric.WithDescription("Sampled spans not delivered since process start, by reason: queue_full, export_failed or shutdown."))
	if err != nil {
		return err
	}
	failures, err := meter.Int64ObservableCounter("jelee.tracing.export_failures", metric.WithDescription("OTLP export calls that failed after their retries since process start."))
	if err != nil {
		return err
	}
	sets := make([]metric.ObserveOption, len(reasons))
	for i, reason := range reasons {
		sets[i] = metric.WithAttributeSet(attribute.NewSet(attribute.String("reason", reason)))
	}
	_, err = meter.RegisterCallback(func(ctx context.Context, observer metric.Observer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !m.stopped.Load() {
			observer.ObserveInt64(exported, counterValue(source.Exported()))
			observer.ObserveInt64(failures, counterValue(source.ExportFailures()))
			for i, reason := range reasons {
				observer.ObserveInt64(dropped, counterValue(source.Dropped(reason)), sets[i])
			}
		}
		return nil
	}, exported, dropped, failures)
	return err
}
