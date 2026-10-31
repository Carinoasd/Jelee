package telemetry

import (
	"testing"
)

type traceExportTestCounters struct{ exported, failures, full, failed, stopped uint64 }

func (c traceExportTestCounters) Exported() uint64       { return c.exported }
func (c traceExportTestCounters) ExportFailures() uint64 { return c.failures }
func (c traceExportTestCounters) Dropped(reason string) uint64 {
	switch reason {
	case "queue_full":
		return c.full
	case "export_failed":
		return c.failed
	case "shutdown":
		return c.stopped
	}
	return 0
}

func TestTraceExportCountersAreExposed(t *testing.T) {
	m := newImageTestExporter(t, nil)
	families := scrapeOps(t, m)
	for _, name := range []string{"jelee_tracing_spans_exported_total", "jelee_tracing_spans_dropped_total", "jelee_tracing_export_failures_total"} {
		if families[name] != nil {
			t.Fatalf("%s exposed while trace export is not registered", name)
		}
	}
	reasons := []string{"queue_full", "export_failed", "shutdown"}
	if err := m.RegisterTraceExport(traceExportTestCounters{exported: 40, failures: 2, full: 7, failed: 5, stopped: 1 << 63}, reasons); err != nil {
		t.Fatal(err)
	}
	families = scrapeOps(t, m)
	checks := []struct {
		name   string
		labels map[string]string
		want   float64
	}{
		{"jelee_tracing_spans_exported_total", map[string]string{}, 40},
		{"jelee_tracing_export_failures_total", map[string]string{}, 2},
		{"jelee_tracing_spans_dropped_total", map[string]string{"reason": "queue_full"}, 7},
		{"jelee_tracing_spans_dropped_total", map[string]string{"reason": "export_failed"}, 5},
		{"jelee_tracing_spans_dropped_total", map[string]string{"reason": "shutdown"}, 1<<63 - 1},
	}
	for _, c := range checks {
		if got := opsValue(t, families, c.name, c.labels); got != c.want {
			t.Fatalf("%s%v = %v, want %v", c.name, c.labels, got, c.want)
		}
	}
	if len(families["jelee_tracing_spans_dropped_total"].Metric) != len(reasons) {
		t.Fatal("dropped counter has points beyond the registered reasons")
	}
	if err := m.RegisterTraceExport(nil, reasons); err == nil {
		t.Fatal("nil trace export source registered")
	}
	if err := m.RegisterTraceExport(traceExportTestCounters{}, nil); err == nil {
		t.Fatal("trace export registered without reasons")
	}
}
