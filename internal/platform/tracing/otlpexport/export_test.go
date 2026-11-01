package otlpexport

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
	"github.com/MoYuanCN/Jelee/internal/platform/tracing"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// receiver is a fake OTLP/HTTP collector: it decodes every export request
// and answers with status (200 unless set).
type receiver struct {
	mu       sync.Mutex
	spans    []*tracepb.Span
	resource map[string]string
	headers  []http.Header
	paths    []string
	status   atomic.Int32
	requests atomic.Int32
	server   *httptest.Server
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()
	r := &receiver{}
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.requests.Add(1)
		body := io.Reader(req.Body)
		if req.Header.Get("Content-Encoding") == "gzip" {
			gz, err := gzip.NewReader(req.Body)
			if err != nil {
				http.Error(w, "gzip", http.StatusBadRequest)
				return
			}
			body = gz
		}
		data, err := io.ReadAll(body)
		if err != nil {
			http.Error(w, "read", http.StatusBadRequest)
			return
		}
		var export coltracepb.ExportTraceServiceRequest
		if err := proto.Unmarshal(data, &export); err != nil {
			http.Error(w, "decode", http.StatusBadRequest)
			return
		}
		if status := r.status.Load(); status != 0 {
			http.Error(w, "refused", int(status))
			return
		}
		r.mu.Lock()
		r.headers = append(r.headers, req.Header.Clone())
		r.paths = append(r.paths, req.URL.Path)
		for _, rs := range export.ResourceSpans {
			r.resource = map[string]string{}
			for _, kv := range rs.GetResource().GetAttributes() {
				r.resource[kv.Key] = kv.GetValue().GetStringValue()
			}
			for _, ss := range rs.ScopeSpans {
				r.spans = append(r.spans, ss.Spans...)
			}
		}
		r.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(r.server.Close)
	return r
}

func (r *receiver) received() []*tracepb.Span {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*tracepb.Span(nil), r.spans...)
}

func newTestExporter(t *testing.T, endpoint string, mutate func(*Options)) *Exporter {
	t.Helper()
	o := Options{OTLPSettings: tracing.OTLPSettings{Endpoint: endpoint, Insecure: true, BatchSize: 64, Interval: 100 * time.Millisecond, Timeout: 2 * time.Second}}
	if mutate != nil {
		mutate(&o)
	}
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Shutdown(context.Background()) })
	return e
}

func newTracer(t *testing.T, rate float64, exporter tracing.Exporter, logs io.Writer) *tracing.Tracer {
	t.Helper()
	tracer, err := tracing.New(tracing.Options{SampleRate: rate, Logger: logging.New(logs), Links: tracing.NewLinks(8), Exporter: exporter})
	if err != nil {
		t.Fatal(err)
	}
	return tracer
}

func attributes(span *tracepb.Span) map[string]string {
	out := map[string]string{}
	for _, kv := range span.Attributes {
		v := kv.GetValue()
		switch {
		case v.GetStringValue() != "":
			out[kv.Key] = v.GetStringValue()
		default:
			if v.GetBoolValue() {
				out[kv.Key] = "true"
			} else {
				out[kv.Key] = "false"
			}
		}
	}
	return out
}

func TestExportKeepsLogIdentifiersAndLinks(t *testing.T) {
	recv := newReceiver(t)
	e := newTestExporter(t, recv.server.URL, func(o *Options) {
		o.Headers = map[string]string{"Authorization": "Bearer collector-token"}
	})
	var logs bytes.Buffer
	tracer := newTracer(t, 1, e, &logs)
	const requestID = "4bf92f3577b34da6a3ce929d0e0e4736"
	ctx, root := tracer.StartRequest(context.Background(), requestID, "http.request", "http")
	childCtx, child := tracer.Start(ctx, "media.direct", "media")
	_ = childCtx
	tracer.Remember(ctx, tracing.LinkJob, "job-1")
	_, job := tracer.StartLinked(context.Background(), "job.inventory_scan", "scan", tracing.LinkJob, "job-1")
	_, orphan := tracer.StartLinked(context.Background(), "webhook.deliver", "webhook", tracing.LinkWebhookEvent, "unknown")
	child.End("")
	job.End("failed")
	orphan.End("pending")
	root.End("ok")
	if err := e.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	spans := recv.received()
	if len(spans) != 4 || e.Exported() != 4 {
		t.Fatalf("exported %d spans, counter %d", len(spans), e.Exported())
	}
	byName := map[string]*tracepb.Span{}
	for _, s := range spans {
		byName[s.Name] = s
	}
	hexOf := func(b []byte) string { return hex.EncodeToString(b) }
	rootSpan := byName["http.request"]
	if hexOf(rootSpan.TraceId) != requestID || hexOf(rootSpan.SpanId) != root.Context().SpanHex() || len(rootSpan.ParentSpanId) != 0 || rootSpan.Kind != tracepb.Span_SPAN_KIND_SERVER {
		t.Fatalf("root span %+v", rootSpan)
	}
	childSpan := byName["media.direct"]
	if hexOf(childSpan.TraceId) != requestID || hexOf(childSpan.ParentSpanId) != root.Context().SpanHex() || hexOf(childSpan.SpanId) != child.Context().SpanHex() {
		t.Fatalf("child span %+v", childSpan)
	}
	jobSpan := byName["job.inventory_scan"]
	if hexOf(jobSpan.TraceId) != requestID || hexOf(jobSpan.ParentSpanId) != root.Context().SpanHex() || jobSpan.Kind != tracepb.Span_SPAN_KIND_CONSUMER {
		t.Fatalf("linked job span %+v", jobSpan)
	}
	if len(jobSpan.Links) != 1 || hexOf(jobSpan.Links[0].TraceId) != requestID || hexOf(jobSpan.Links[0].SpanId) != root.Context().SpanHex() {
		t.Fatalf("job link %+v", jobSpan.Links)
	}
	if jobSpan.Status.GetCode() != tracepb.Status_STATUS_CODE_ERROR || rootSpan.Status.GetCode() != tracepb.Status_STATUS_CODE_UNSET {
		t.Fatalf("status job %v root %v", jobSpan.Status, rootSpan.Status)
	}
	if got := attributes(jobSpan); got["jelee.component"] != "scan" || got["jelee.outcome"] != "failed" || got["jelee.linkKind"] != "job" || got["jelee.linked"] != "true" || got["jelee.forced"] != "false" || len(got) != 5 {
		t.Fatalf("job attributes %v", got)
	}
	orphanSpan := byName["webhook.deliver"]
	if len(orphanSpan.Links) != 0 || len(orphanSpan.ParentSpanId) != 0 || attributes(orphanSpan)["jelee.linked"] != "false" || orphanSpan.Status.GetCode() != tracepb.Status_STATUS_CODE_ERROR || orphanSpan.Kind != tracepb.Span_SPAN_KIND_CLIENT {
		t.Fatalf("unlinked webhook span %+v", orphanSpan)
	}
	if hexOf(orphanSpan.TraceId) != orphan.Context().TraceHex() {
		t.Fatal("unlinked span trace differs from its log trace")
	}
	// Times come from the tracer: end is after start.
	if rootSpan.EndTimeUnixNano < rootSpan.StartTimeUnixNano || rootSpan.StartTimeUnixNano == 0 {
		t.Fatal("span times")
	}
	// The log records name the same identifiers.
	for _, s := range spans {
		if !strings.Contains(logs.String(), `"spanId":"`+hexOf(s.SpanId)+`"`) {
			t.Fatalf("span %s has no log record with its spanId", s.Name)
		}
	}
	recv.mu.Lock()
	defer recv.mu.Unlock()
	if recv.resource["service.name"] != "jelee" || len(recv.resource) != 2 {
		t.Fatalf("resource %v", recv.resource)
	}
	if recv.headers[0].Get("Authorization") != "Bearer collector-token" || recv.paths[0] != tracing.OTLPDefaultPath {
		t.Fatalf("request headers %v path %v", recv.headers[0], recv.paths)
	}
}

func TestExportAttributesFollowLogWhitelist(t *testing.T) {
	trace, _ := domain.ParseTraceID("0123456789abcdef0123456789abcdef")
	span, _ := domain.NewSpanID()
	sc := domain.NewRootSpanContext(trace, span, true)
	converted := newConverter().span(tracing.SpanRecord{Name: "http.request /library/secret.mkv", Context: sc, Start: time.Unix(1, 0), End: time.Unix(2, 0),
		Attrs: []slog.Attr{
			slog.String("component", "http"),
			slog.String("outcome", "Bearer abc.def"),
			slog.String("path", "/media/private/movie.mkv"),
			slog.String("clientIp", "203.0.113.9"),
			slog.String("linkKind", "job"),
			slog.String("component2", "x"),
			slog.Int64("durationMs", 7),
		}})
	if converted.Name() != "redacted" {
		t.Fatalf("unsafe span name exported: %q", converted.Name())
	}
	got := map[string]string{}
	for _, kv := range converted.Attributes() {
		got[string(kv.Key)] = kv.Value.String()
	}
	want := map[string]string{"jelee.component": "http", "jelee.outcome": "[redacted]", "jelee.linkKind": "job"}
	if len(got) != len(want) {
		t.Fatalf("attributes %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("attribute %s = %q, want %q (all %v)", k, got[k], v, got)
		}
	}
	for _, kv := range converted.Resource().Attributes() {
		if kv.Key != "service.name" && kv.Key != "service.version" {
			t.Fatalf("resource attribute %s exported", kv.Key)
		}
	}
}

func TestExportFollowsLogSampling(t *testing.T) {
	recv := newReceiver(t)
	e := newTestExporter(t, recv.server.URL, nil)
	var logs bytes.Buffer
	tracer := newTracer(t, 0, e, &logs)
	// Unsampled: neither a span record nor an exported span.
	_, plain := tracer.StartRequest(context.Background(), "", "http.request", "http")
	plain.End("ok")
	// A security event forces the trace: both appear.
	ctx, forced := tracer.StartRequest(context.Background(), "", "http.request", "http")
	_, child := tracer.Start(ctx, "media.direct", "media")
	domain.ForceTraceSampling(ctx)
	child.End("")
	forced.End("ok")
	if err := e.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	records := strings.Count(logs.String(), `"msg":"span completed"`)
	spans := recv.received()
	if records != 2 || len(spans) != records {
		t.Fatalf("%d span records, %d exported spans", records, len(spans))
	}
	for _, s := range spans {
		if hex.EncodeToString(s.TraceId) != forced.Context().TraceHex() || attributes(s)["jelee.forced"] != "true" {
			t.Fatalf("exported span %s not of the forced trace", s.Name)
		}
	}
}

func TestEnvironmentCannotRedirectExport(t *testing.T) {
	recv := newReceiver(t)
	other := newReceiver(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", other.server.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", other.server.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "x-leak=1")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "host.name=secret-host")
	t.Setenv("HTTP_PROXY", other.server.URL)
	e := newTestExporter(t, recv.server.URL+"/custom/traces", nil)
	tracer := newTracer(t, 1, e, io.Discard)
	_, span := tracer.StartRequest(context.Background(), "", "http.request", "http")
	span.End("ok")
	if err := e.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if other.requests.Load() != 0 || len(recv.received()) != 1 {
		t.Fatalf("configured %d spans, environment target %d requests", len(recv.received()), other.requests.Load())
	}
	recv.mu.Lock()
	defer recv.mu.Unlock()
	if recv.headers[0].Get("x-leak") != "" || recv.paths[0] != "/custom/traces" || len(recv.resource) != 2 {
		t.Fatalf("environment leaked: headers %v path %v resource %v", recv.headers[0], recv.paths[0], recv.resource)
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	target := newReceiver(t)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.server.URL+"/v1/traces", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)
	e := newTestExporter(t, redirect.URL, func(o *Options) { o.Headers = map[string]string{"Authorization": "Bearer t"} })
	tracer := newTracer(t, 1, e, io.Discard)
	_, span := tracer.StartRequest(context.Background(), "", "http.request", "http")
	span.End("ok")
	_ = e.Shutdown(context.Background())
	if target.requests.Load() != 0 || e.ExportFailures() != 1 || e.Dropped(DropExportFailed) != 1 {
		t.Fatalf("redirect followed: target %d, failures %d, dropped %d", target.requests.Load(), e.ExportFailures(), e.Dropped(DropExportFailed))
	}
}

func TestExportFailureIsCountedAndWarnedWithoutDetails(t *testing.T) {
	recv := newReceiver(t)
	recv.status.Store(http.StatusBadRequest)
	var logs bytes.Buffer
	e := newTestExporter(t, recv.server.URL, func(o *Options) {
		o.Logger = logging.New(&logs)
		o.Headers = map[string]string{"Authorization": "Bearer secret-collector-token"}
		o.BatchSize = 1
	})
	tracer := newTracer(t, 1, e, io.Discard)
	for range 3 {
		_, span := tracer.StartRequest(context.Background(), "", "http.request", "http")
		span.End("ok")
	}
	_ = e.Shutdown(context.Background())
	if e.ExportFailures() != 3 || e.Dropped(DropExportFailed) != 3 || e.Exported() != 0 {
		t.Fatalf("failures %d dropped %d exported %d", e.ExportFailures(), e.Dropped(DropExportFailed), e.Exported())
	}
	if n := strings.Count(logs.String(), "trace export failed"); n != 1 {
		t.Fatalf("%d warnings, want one per minute: %s", n, logs.String())
	}
	for _, leak := range []string{"secret-collector-token", "127.0.0.1", "refused", "/v1/traces"} {
		if strings.Contains(logs.String(), leak) {
			t.Fatalf("warning leaks %q: %s", leak, logs.String())
		}
	}
}

// blockingExporter holds every export until release closes.
type blockingExporter struct {
	release  chan struct{}
	exported atomic.Int64
	calls    atomic.Int64
}

func (b *blockingExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	b.calls.Add(1)
	select {
	case <-b.release:
		b.exported.Add(int64(len(spans)))
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *blockingExporter) Shutdown(context.Context) error { return nil }

func record() tracing.SpanRecord {
	trace, _ := domain.NewTraceID()
	span, _ := domain.NewSpanID()
	return tracing.SpanRecord{Name: "http.request", Context: domain.NewRootSpanContext(trace, span, true), Start: time.Now(), End: time.Now()}
}

func TestFullQueueDropsWithoutBlocking(t *testing.T) {
	block := &blockingExporter{release: make(chan struct{})}
	e := start(Options{OTLPSettings: tracing.OTLPSettings{Timeout: time.Minute, BatchSize: 1, Interval: time.Hour, QueueSize: 4}}, block)
	// The loop takes one record and blocks exporting it; four more fill
	// the queue and the rest are dropped at once.
	e.ExportSpan(record())
	deadline := time.Now().Add(5 * time.Second)
	for block.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	started := time.Now()
	for range 100 {
		e.ExportSpan(record())
	}
	if time.Since(started) > time.Second {
		t.Fatal("ExportSpan blocked on a full queue")
	}
	if e.Dropped(DropQueueFull) != 96 {
		t.Fatalf("dropped %d, want 96", e.Dropped(DropQueueFull))
	}
	close(block.release)
	if err := e.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if block.exported.Load() != 5 || e.Exported() != 5 {
		t.Fatalf("exported %d/%d after release", block.exported.Load(), e.Exported())
	}
}

func TestShutdownDeadlineDropsQueuedSpans(t *testing.T) {
	block := &blockingExporter{release: make(chan struct{})}
	e := start(Options{OTLPSettings: tracing.OTLPSettings{Timeout: time.Minute, BatchSize: 2, Interval: time.Hour, QueueSize: 16}}, block)
	for range 5 {
		e.ExportSpan(record())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := e.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error %v", err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("shutdown ignored its deadline")
	}
	<-e.done
	// Every accepted span is either exported or counted; none exported here.
	if got := e.Dropped(DropShutdown) + e.Dropped(DropExportFailed); got != 5 || e.Exported() != 0 {
		t.Fatalf("lost %d of 5 (exported %d)", got, e.Exported())
	}
	e.ExportSpan(record())
	if e.Dropped(DropShutdown)+e.Dropped(DropExportFailed) != 6 {
		t.Fatal("span after shutdown not counted")
	}
	if e.Dropped("other") != 0 {
		t.Fatal("unknown reason counted")
	}
}

func TestNewRefusesUnsafeSettings(t *testing.T) {
	for name, s := range map[string]tracing.OTLPSettings{
		"remote http":        {Endpoint: "http://collector.example:4318", Insecure: true},
		"http not insecure":  {Endpoint: "http://127.0.0.1:4318"},
		"https insecure":     {Endpoint: "https://collector.example", Insecure: true},
		"loopback name":      {Endpoint: "http://localhost:4318", Insecure: true},
		"credentials":        {Endpoint: "https://user:pw@collector.example"},
		"query":              {Endpoint: "https://collector.example/v1/traces?token=x"},
		"fragment":           {Endpoint: "https://collector.example/#x"},
		"scheme":             {Endpoint: "grpc://collector.example"},
		"relative":           {Endpoint: "/v1/traces"},
		"header newline":     {Endpoint: "https://c.example", Headers: map[string]string{"Authorization": "a\r\nX: y"}},
		"header name":        {Endpoint: "https://c.example", Headers: map[string]string{"Bad Name": "v"}},
		"reserved header":    {Endpoint: "https://c.example", Headers: map[string]string{"Content-Type": "text/plain"}},
		"empty header value": {Endpoint: "https://c.example", Headers: map[string]string{"X-Tenant": ""}},
		"header bytes":       {Endpoint: "https://c.example", Headers: map[string]string{"X-Big": strings.Repeat("a", tracing.OTLPMaxHeaderBytes)}},
		"timeout":            {Endpoint: "https://c.example", Timeout: time.Hour},
		"batch":              {Endpoint: "https://c.example", BatchSize: tracing.OTLPMaxBatchSize + 1},
		"interval":           {Endpoint: "https://c.example", Interval: time.Millisecond},
		"queue":              {Endpoint: "https://c.example", QueueSize: -1},
	} {
		if _, err := New(Options{OTLPSettings: s}); err == nil {
			t.Fatalf("%s accepted", name)
		} else if strings.Contains(err.Error(), "example") || strings.Contains(err.Error(), "pw") {
			t.Fatalf("%s error echoes the setting: %v", name, err)
		}
	}
	headers := map[string]string{}
	for i := range tracing.OTLPMaxHeaders + 1 {
		headers["X-H"+string(rune('a'+i))] = "v"
	}
	if tracing.ValidateOTLPHeaders(headers) == nil {
		t.Fatal("too many headers accepted")
	}
	for _, ok := range []string{"http://127.0.0.1:4318", "http://[::1]:4318/v1/traces", "http://127.8.9.10"} {
		if err := tracing.ValidateOTLPEndpoint(ok, true); err != nil {
			t.Fatalf("%s refused: %v", ok, err)
		}
	}
	e, err := New(Options{OTLPSettings: tracing.OTLPSettings{Endpoint: "https://collector.example:4318"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPClientIgnoresProxiesAndPinsTLS(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://192.0.2.1:3128")
	client := newHTTPClient(time.Second, nil)
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil || transport.TLSClientConfig == nil || transport.TLSClientConfig.InsecureSkipVerify ||
		transport.TLSClientConfig.MinVersion < tls.VersionTLS12 || transport.TLSClientConfig.RootCAs != nil || client.CheckRedirect == nil || client.Timeout != time.Second {
		t.Fatalf("client %+v transport %+v", client, transport)
	}
}

func TestSpanKinds(t *testing.T) {
	for name, want := range map[string]string{"http.request": "Server", "webhook.deliver": "Client", "job.nfo_write": "Consumer", "nfo.write": "Internal", "scan.inventory": "Internal"} {
		if got := spanKind(name).String(); !strings.EqualFold(got, want) {
			t.Fatalf("%s kind %s, want %s", name, got, want)
		}
	}
}

func BenchmarkRequestSpanExport(b *testing.B) {
	recv := &receiver{}
	recv.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer recv.server.Close()
	e, err := New(Options{OTLPSettings: tracing.OTLPSettings{Endpoint: recv.server.URL, Insecure: true}})
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = e.Shutdown(context.Background()) }()
	tracer, err := tracing.New(tracing.Options{SampleRate: 1, Logger: logging.New(io.Discard), Links: tracing.NewLinks(8), Exporter: e})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		_, span := tracer.StartRequest(context.Background(), "0123456789abcdef0123456789abcdef", "http.request", "http")
		span.End("ok")
	}
}
