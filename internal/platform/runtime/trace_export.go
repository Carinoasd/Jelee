package runtime

import (
	"context"
	"log/slog"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/tracing"
	"github.com/MoYuanCN/Jelee/internal/platform/tracing/otlpexport"
)

// traceFlushTimeout bounds the export of the spans still queued at
// shutdown; what is left after it is dropped and counted.
const traceFlushTimeout = 5 * time.Second

// newTraceExporter starts the OTLP span exporter when tracing.exporter is
// otlp (G46.6). With export off it returns nil and the tracer pays nothing.
func newTraceExporter(c config.TracingConfig, logger *slog.Logger) (*otlpexport.Exporter, error) {
	if !c.Enabled() {
		return nil, nil
	}
	return otlpexport.New(otlpexport.Options{OTLPSettings: c.OTLP.Settings(), Logger: logger})
}

// spanExporter avoids handing the tracer a typed nil.
func spanExporter(e *otlpexport.Exporter) tracing.Exporter {
	if e == nil {
		return nil
	}
	return e
}

// flushTraces exports the queued spans once no request, job or delivery
// can end another one. A flush that runs out of time loses spans, not the
// clean exit: the loss is counted and logged.
func (l *lifetime) flushTraces() {
	if l.traceExport == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), traceFlushTimeout)
	defer cancel()
	if err := l.traceExport.Shutdown(ctx); err != nil {
		l.logger.Warn("trace export flush did not finish; queued spans dropped", "component", "system", "code", "trace_export_failed")
	}
}
