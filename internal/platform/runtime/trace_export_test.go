package runtime

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
	"github.com/MoYuanCN/Jelee/internal/platform/tracing"
)

func TestTraceExporterOffByDefault(t *testing.T) {
	exporter, err := newTraceExporter(config.TracingConfig{}, slog.New(slog.DiscardHandler))
	if err != nil || exporter != nil {
		t.Fatalf("exporter %v err %v", exporter, err)
	}
	if spanExporter(exporter) != nil {
		t.Fatal("a nil exporter reached the tracer as a typed nil")
	}
	l := newLifetime(slog.New(slog.DiscardHandler))
	l.flushTraces()
}

func TestTraceExporterFlushedAtShutdown(t *testing.T) {
	var received atomic.Int32
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	c := config.TracingConfig{Exporter: config.TraceExporterOTLP, OTLP: config.OTLPTraceConfig{Endpoint: collector.URL, Insecure: true, BatchIntervalMs: 60000}}
	var logs bytes.Buffer
	l := newLifetime(logging.New(&logs))
	exporter, err := newTraceExporter(c, l.logger)
	if err != nil || exporter == nil {
		t.Fatalf("exporter %v err %v", exporter, err)
	}
	l.traceExport = exporter
	tracer, err := tracing.New(tracing.Options{SampleRate: 1, Exporter: spanExporter(exporter)})
	if err != nil {
		t.Fatal(err)
	}
	_, span := tracer.StartRequest(context.Background(), "", "http.request", "http")
	span.End("ok")
	// The batch interval is a minute: only the shutdown flush sends it.
	l.flushTraces()
	if received.Load() != 1 || exporter.Exported() != 1 {
		t.Fatalf("flush sent %d requests, exported %d", received.Load(), exporter.Exported())
	}
	if strings.Contains(logs.String(), "trace export") {
		t.Fatalf("clean flush warned: %s", logs.String())
	}
	if _, err := newTraceExporter(config.TracingConfig{Exporter: config.TraceExporterOTLP, OTLP: config.OTLPTraceConfig{Endpoint: "http://192.0.2.1:4318", Insecure: true}}, l.logger); err == nil {
		t.Fatal("remote plain http accepted at runtime")
	}
}
