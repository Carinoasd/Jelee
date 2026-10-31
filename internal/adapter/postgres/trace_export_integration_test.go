package postgres

import (
	"compress/gzip"
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/platform/tracing"
	"github.com/MoYuanCN/Jelee/internal/platform/tracing/otlpexport"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// TestTraceExportHTTPScanJobPostgres (G46.6, principle 5): with OTLP export
// on, the spans of a scan submitted over HTTP reach a local OTLP receiver
// with the identifiers of their log records: the request ID is the trace ID
// of the request, job and scan spans, the job span is the child of the
// request span and links to it, and every exported span has a span record.
func TestTraceExportHTTPScanJobPostgres(t *testing.T) {
	var mu sync.Mutex
	var spans []*tracepb.Span
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			http.Error(w, "gzip", http.StatusBadRequest)
			return
		}
		data, err := io.ReadAll(gz)
		var export coltracepb.ExportTraceServiceRequest
		if err != nil || proto.Unmarshal(data, &export) != nil {
			http.Error(w, "decode", http.StatusBadRequest)
			return
		}
		mu.Lock()
		for _, rs := range export.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				spans = append(spans, ss.Spans...)
			}
		}
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	exporter, err := otlpexport.New(otlpexport.Options{OTLPSettings: tracing.OTLPSettings{Endpoint: collector.URL, Insecure: true}})
	if err != nil {
		t.Fatal(err)
	}
	logs := &traceLogBuffer{}
	installExportingTracer(t, 1, logs, exporter)
	requestID, _ := scanThroughHTTP(t, logs)
	if err := exporter.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if exporter.Dropped(otlpexport.DropExportFailed)+exporter.Dropped(otlpexport.DropQueueFull) != 0 {
		t.Fatal("spans dropped on the way to the local collector")
	}
	records := map[string]map[string]any{}
	for _, record := range logs.records(t) {
		if record["msg"] == "span completed" {
			records[record["spanId"].(string)] = record
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(spans) != len(records) {
		t.Fatalf("%d spans exported, %d span records", len(spans), len(records))
	}
	byName := map[string]*tracepb.Span{}
	for _, s := range spans {
		record := records[hex.EncodeToString(s.SpanId)]
		if record == nil || record["traceId"] != hex.EncodeToString(s.TraceId) || record["span"] != s.Name {
			t.Fatalf("exported span %s %x has no matching span record", s.Name, s.SpanId)
		}
		if parent, _ := record["parentSpanId"].(string); parent != hex.EncodeToString(s.ParentSpanId) {
			t.Fatalf("span %s parent %x, record %q", s.Name, s.ParentSpanId, parent)
		}
		if hex.EncodeToString(s.TraceId) == requestID {
			byName[s.Name] = s
		}
	}
	request, job, stage := byName["http.request"], byName["job.inventory_scan"], byName["scan.inventory"]
	if request == nil || job == nil || stage == nil {
		t.Fatalf("request trace spans %v", byName)
	}
	if string(job.ParentSpanId) != string(request.SpanId) || len(job.Links) != 1 || string(job.Links[0].SpanId) != string(request.SpanId) || string(stage.ParentSpanId) != string(job.SpanId) {
		t.Fatal("job and scan spans are not joined to the request span")
	}
}
