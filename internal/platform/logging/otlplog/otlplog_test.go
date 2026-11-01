package otlplog

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/buildinfo"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
	"github.com/MoYuanCN/Jelee/internal/platform/tlstrust/tlstrusttest"
	collogpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logpb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/protobuf/proto"
)

const (
	traceHex = "4bf92f3577b34da6a3ce929d0e0e4736"
	spanHex  = "00f067aa0ba902b7"
)

// receiver is a fake OTLP/HTTP logs endpoint. It answers the scripted
// statuses in order, then 200.
type receiver struct {
	*httptest.Server
	mu         sync.Mutex
	script     []int
	retryAfter string
	requests   []*collogpb.ExportLogsServiceRequest
	raw        [][]byte
	headers    []http.Header
	paths      []string
	times      []time.Time
	attempts   atomic.Int64
	hang       chan struct{}
}

func newReceiver(t *testing.T, script ...int) *receiver {
	t.Helper()
	r := &receiver{script: script}
	r.Server = httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.Close)
	return r
}

func (r *receiver) serve(w http.ResponseWriter, req *http.Request) {
	r.attempts.Add(1)
	if r.hang != nil {
		select {
		case <-r.hang:
		case <-req.Context().Done():
			return
		}
	}
	body, _ := io.ReadAll(req.Body)
	if req.Header.Get("Content-Encoding") == "gzip" {
		if zr, err := gzip.NewReader(bytes.NewReader(body)); err == nil {
			body, _ = io.ReadAll(zr)
		}
	}
	var msg collogpb.ExportLogsServiceRequest
	_ = proto.Unmarshal(body, &msg)
	r.mu.Lock()
	code := http.StatusOK
	if len(r.script) > 0 {
		code, r.script = r.script[0], r.script[1:]
	}
	r.times = append(r.times, time.Now())
	if code == http.StatusOK {
		r.requests = append(r.requests, &msg)
		r.raw = append(r.raw, body)
		r.headers = append(r.headers, req.Header.Clone())
		r.paths = append(r.paths, req.URL.Path)
	}
	retryAfter := r.retryAfter
	r.mu.Unlock()
	if retryAfter != "" && code != http.StatusOK {
		w.Header().Set("Retry-After", retryAfter)
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(code)
}

func (r *receiver) records() []*logpb.LogRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*logpb.LogRecord
	for _, req := range r.requests {
		for _, rl := range req.ResourceLogs {
			for _, sl := range rl.ScopeLogs {
				out = append(out, sl.LogRecords...)
			}
		}
	}
	return out
}

func otlpOptions(endpoint string) logging.ForwarderOptions {
	return logging.ForwarderOptions{Kind: logging.ForwardOTLP, BatchInterval: 10 * time.Millisecond,
		OTLP: logging.OTLPOptions{Endpoint: endpoint, Insecure: strings.HasPrefix(endpoint, "http:")}}
}

func newForwarder(t *testing.T, o logging.ForwarderOptions) logging.Forwarder {
	t.Helper()
	f, err := logging.NewForwarder(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// captured builds a forwarder and returns its sender too, so a test can
// push one batch synchronously and see the classified error.
func captured(t *testing.T, o logging.ForwarderOptions) logging.ForwardSender {
	t.Helper()
	var got logging.ForwardSender
	logging.RegisterOTLPSender(func(target logging.OTLPTarget) (logging.ForwardSender, error) {
		s, err := New(target)
		got = s
		return s, err
	})
	t.Cleanup(func() { logging.RegisterOTLPSender(New) })
	newForwarder(t, o)
	if got == nil {
		t.Fatal("sender not built")
	}
	return got
}

func stat(f logging.Forwarder) logging.ForwardStat {
	return f.(interface{ ForwardStat() logging.ForwardStat }).ForwardStat()
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func attrs(kvs []*commonpb.KeyValue) map[string]*commonpb.AnyValue {
	m := map[string]*commonpb.AnyValue{}
	for _, kv := range kvs {
		m[kv.Key] = kv.Value
	}
	return m
}

const sample = `{"time":"2026-10-31T08:09:10.123456789Z","level":"INFO","msg":"request completed","component":"http","status":200,"media":false,"ratio":0.5,"traceId":"` + traceHex + `","spanId":"` + spanHex + `","parentSpanId":"aaaaaaaaaaaaaaaa","request":{"method":"GET","route":"/api/v1/items/{id}"},"tags":["a","b"],"missing":null}`

func TestRecordFieldsSeverityAndTraceContext(t *testing.T) {
	r := newReceiver(t)
	f := newForwarder(t, otlpOptions(r.URL))
	for _, line := range []string{
		sample + "\n",
		`{"time":"2026-10-31T08:09:11Z","level":"DEBUG","msg":"d","component":"scan"}`,
		`{"time":"2026-10-31T08:09:12Z","level":"WARN","msg":"w","traceId":"not-a-trace","spanId":"00f067aa0ba902b7"}`,
		`{"time":"2026-10-31T08:09:13Z","level":"ERROR+2","msg":"e"}`,
	} {
		_, _ = f.Write([]byte(line))
	}
	if err := f.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	recs := r.records()
	if len(recs) != 4 {
		t.Fatalf("%d records", len(recs))
	}
	first := recs[0]
	if first.Body.GetStringValue() != "request completed" || first.SeverityNumber != logpb.SeverityNumber_SEVERITY_NUMBER_INFO || first.SeverityText != "INFO" {
		t.Fatalf("body/severity %v %v %q", first.Body, first.SeverityNumber, first.SeverityText)
	}
	want, _ := time.Parse(time.RFC3339Nano, "2026-10-31T08:09:10.123456789Z")
	if first.TimeUnixNano != uint64(want.UnixNano()) || first.ObservedTimeUnixNano == 0 {
		t.Fatalf("time %d observed %d", first.TimeUnixNano, first.ObservedTimeUnixNano)
	}
	if hex.EncodeToString(first.TraceId) != traceHex || hex.EncodeToString(first.SpanId) != spanHex {
		t.Fatalf("trace %x span %x", first.TraceId, first.SpanId)
	}
	a := attrs(first.Attributes)
	for _, key := range []string{"time", "level", "msg", "traceId", "spanId"} {
		if _, ok := a[key]; ok {
			t.Errorf("%s duplicated as an attribute", key)
		}
	}
	if a["component"].GetStringValue() != "http" || a["status"].GetIntValue() != 200 || a["media"] == nil || a["media"].GetBoolValue() ||
		a["ratio"].GetDoubleValue() != 0.5 || a["parentSpanId"].GetStringValue() != "aaaaaaaaaaaaaaaa" {
		t.Fatalf("attributes %v", a)
	}
	group := attrs(a["request"].GetKvlistValue().GetValues())
	if group["method"].GetStringValue() != "GET" || group["route"].GetStringValue() != "/api/v1/items/{id}" {
		t.Fatalf("group %v", a["request"])
	}
	if tags := a["tags"].GetArrayValue().GetValues(); len(tags) != 2 || tags[1].GetStringValue() != "b" {
		t.Fatalf("tags %v", a["tags"])
	}
	if recs[1].SeverityNumber != logpb.SeverityNumber_SEVERITY_NUMBER_DEBUG || attrs(recs[1].Attributes)["component"].GetStringValue() != "scan" {
		t.Fatalf("debug record %v", recs[1])
	}
	// An invalid traceId is kept as an attribute and sets no trace context.
	if recs[2].SeverityNumber != logpb.SeverityNumber_SEVERITY_NUMBER_WARN || len(recs[2].TraceId) != 0 || len(recs[2].SpanId) != 0 ||
		attrs(recs[2].Attributes)["traceId"].GetStringValue() != "not-a-trace" {
		t.Fatalf("warn record %v", recs[2])
	}
	if recs[3].SeverityNumber != logpb.SeverityNumber_SEVERITY_NUMBER_ERROR3 || recs[3].SeverityText != "ERROR+2" {
		t.Fatalf("error record %v %q", recs[3].SeverityNumber, recs[3].SeverityText)
	}
}

func TestRequestShapeResourceAndScope(t *testing.T) {
	r := newReceiver(t)
	o := otlpOptions(r.URL + "/collector/")
	o.OTLP.Headers = map[string]string{"Authorization": "Bearer collector-token", "X-Scope-OrgID": "home"}
	f := newForwarder(t, o)
	_, _ = f.Write([]byte(sample))
	if err := f.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.requests) != 1 {
		t.Fatalf("%d requests", len(r.requests))
	}
	h := r.headers[0]
	if r.paths[0] != "/collector/v1/logs" || h.Get("Content-Type") != "application/x-protobuf" || h.Get("Content-Encoding") != "gzip" ||
		h.Get("Authorization") != "Bearer collector-token" || h.Get("X-Scope-OrgID") != "home" || h.Get("User-Agent") != "jelee-log-forwarder" {
		t.Fatalf("path %q headers %v", r.paths[0], h)
	}
	rl := r.requests[0].ResourceLogs
	if len(rl) != 1 || len(rl[0].ScopeLogs) != 1 {
		t.Fatalf("resource logs %v", rl)
	}
	res := attrs(rl[0].Resource.Attributes)
	if len(res) != 2 || res["service.name"].GetStringValue() != "jelee" || res["service.version"].GetStringValue() != buildinfo.Version() {
		t.Fatalf("resource %v", res)
	}
	if scope := rl[0].ScopeLogs[0].Scope; scope.Name != ScopeName || scope.Version != buildinfo.Version() {
		t.Fatalf("scope %v", scope)
	}
}

func TestStatusDecidesRetry(t *testing.T) {
	other := newReceiver(t)
	cases := []struct {
		code  int
		class string
	}{
		{200, ""}, {400, "rejected 400"}, {401, "rejected 401"}, {413, "rejected 413"},
		{408, "status 408"}, {429, "status 429"}, {500, "status 500"}, {503, "status 503"},
	}
	for _, c := range cases {
		r := newReceiver(t, c.code)
		s := captured(t, otlpOptions(r.URL))
		err := s.Send(context.Background(), [][]byte{[]byte(sample)})
		if c.class == "" {
			if err != nil {
				t.Errorf("%d: %v", c.code, err)
			}
			continue
		}
		if err == nil || !strings.HasSuffix(err.Error(), ": "+c.class) || strings.Contains(err.Error(), r.URL) {
			t.Errorf("%d: %v", c.code, err)
		}
	}
	// A redirect is not followed: the token would reach another host.
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, other.URL+"/v1/logs", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	o := otlpOptions(redirect.URL)
	o.OTLP.Headers = map[string]string{"Authorization": "Bearer t"}
	err := captured(t, o).Send(context.Background(), [][]byte{[]byte(sample)})
	if err == nil || !strings.HasSuffix(err.Error(), "rejected 307") || other.attempts.Load() != 0 {
		t.Fatalf("redirect: %v, other got %d", err, other.attempts.Load())
	}
	// No collector at all is a retryable network failure.
	dead := newReceiver(t)
	s := captured(t, otlpOptions(dead.URL))
	dead.Close()
	if err := s.Send(context.Background(), [][]byte{[]byte(sample)}); err == nil || !strings.HasSuffix(err.Error(), ": network") {
		t.Fatalf("closed collector: %v", err)
	}
}

func TestBackoffRetriesTheBatch(t *testing.T) {
	r := newReceiver(t, 503)
	f := newForwarder(t, otlpOptions(r.URL))
	_, _ = f.Write([]byte(sample))
	eventually(t, "delivery after a retry", func() bool { return stat(f).Sent == 1 })
	st := stat(f)
	r.mu.Lock()
	gap := r.times[1].Sub(r.times[0])
	r.mu.Unlock()
	// The first backoff is 0.5 s with jitter down to half of it.
	if st.Failures != 1 || st.Dropped != 0 || r.attempts.Load() != 2 || gap < 200*time.Millisecond {
		t.Fatalf("stat %+v attempts %d gap %v", st, r.attempts.Load(), gap)
	}
}

func TestRetryAfterIsHonoured(t *testing.T) {
	r := newReceiver(t, 429)
	r.retryAfter = "1"
	f := newForwarder(t, otlpOptions(r.URL))
	_, _ = f.Write([]byte(sample))
	eventually(t, "delivery after Retry-After", func() bool { return stat(f).Sent == 1 })
	r.mu.Lock()
	gap := r.times[1].Sub(r.times[0])
	r.mu.Unlock()
	if gap < 900*time.Millisecond {
		t.Fatalf("retried after %v despite Retry-After: 1", gap)
	}
}

func TestPermanentRejectionIsCountedAsDropped(t *testing.T) {
	r := newReceiver(t, 400)
	f := newForwarder(t, otlpOptions(r.URL))
	_, _ = f.Write([]byte(sample))
	_, _ = f.Write([]byte(sample))
	eventually(t, "drop", func() bool { return stat(f).Dropped == 2 })
	time.Sleep(50 * time.Millisecond)
	if st := stat(f); r.attempts.Load() != 1 || st.Failures != 1 || st.Sent != 0 {
		t.Fatalf("stat %+v attempts %d", st, r.attempts.Load())
	}
}

func TestFullQueueDropsWithoutBlocking(t *testing.T) {
	r := newReceiver(t)
	r.hang = make(chan struct{})
	o := otlpOptions(r.URL)
	o.QueueEntries, o.BatchSize, o.Timeout = 8, 1, 5*time.Second
	f := newForwarder(t, o)
	t.Cleanup(func() { close(r.hang) })
	start := time.Now()
	for range 5000 {
		_, _ = f.Write([]byte(sample))
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Write blocked behind a hung collector: %v", d)
	}
	if st := stat(f); st.Dropped < 4900 {
		t.Fatalf("stat %+v", st)
	}
}

func TestFlushDeadlineCountsUndelivered(t *testing.T) {
	r := newReceiver(t)
	r.hang = make(chan struct{})
	o := otlpOptions(r.URL)
	o.BatchInterval = logging.MaxForwardBatchInterval
	f := newForwarder(t, o)
	for range 3 {
		_, _ = f.Write([]byte(sample))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := f.Flush(ctx); !errors.Is(err, logging.ErrForwardDeadline) {
		t.Fatalf("Flush = %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Flush ignored its deadline: %v", d)
	}
	eventually(t, "drops", func() bool { return stat(f).Dropped == 3 })
	close(r.hang)
	start = time.Now()
	if err := f.Close(); err != nil || time.Since(start) > 2*time.Second {
		t.Fatalf("Close = %v after %v", err, time.Since(start))
	}
}

func TestCloseDeliversQueuedRecords(t *testing.T) {
	r := newReceiver(t)
	o := otlpOptions(r.URL)
	o.BatchSize, o.BatchInterval = 4, logging.MaxForwardBatchInterval
	f := newForwarder(t, o)
	for range 10 {
		_, _ = f.Write([]byte(sample))
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if n := len(r.records()); n != 10 {
		t.Fatalf("%d of 10 records delivered at Close", n)
	}
}

func TestCAFileTrustsOnlyItsCertificates(t *testing.T) {
	ca := tlstrusttest.NewCA(t, "collector CA")
	var got atomic.Int64
	srv := ca.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		got.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	o := otlpOptions(srv.URL)
	o.OTLP.CAFile = ca.WriteFile(t)
	if err := captured(t, o).Send(context.Background(), [][]byte{[]byte(sample)}); err != nil || got.Load() != 1 {
		t.Fatalf("trusted CA: %v (%d requests)", err, got.Load())
	}
	o.OTLP.CAFile = tlstrusttest.NewCA(t, "another CA").WriteFile(t)
	if err := captured(t, o).Send(context.Background(), [][]byte{[]byte(sample)}); err == nil || !strings.HasSuffix(err.Error(), ": tls") {
		t.Fatalf("foreign CA: %v", err)
	}
	o.OTLP.CAFile = ""
	if err := captured(t, o).Send(context.Background(), [][]byte{[]byte(sample)}); err == nil || !strings.HasSuffix(err.Error(), ": tls") {
		t.Fatalf("system roots: %v", err)
	}
	if got.Load() != 1 {
		t.Fatalf("untrusted handshakes reached the handler: %d", got.Load())
	}
	o.OTLP.CAFile = filepath.Join(t.TempDir(), "missing.pem")
	if _, err := logging.NewForwarder(o); err == nil || strings.Contains(err.Error(), o.OTLP.CAFile) {
		t.Fatalf("missing CA file: %v", err)
	}
}

func TestPlainHTTPOnlyToLoopback(t *testing.T) {
	for name, o := range map[string]logging.OTLPOptions{
		"remote insecure":    {Endpoint: "http://192.0.2.10:4318", Insecure: true},
		"name insecure":      {Endpoint: "http://localhost:4318", Insecure: true},
		"loopback no flag":   {Endpoint: "http://127.0.0.1:4318"},
		"https with flag":    {Endpoint: "https://collector.example.org", Insecure: true},
		"ca with plain http": {Endpoint: "http://127.0.0.1:4318", Insecure: true, CAFile: "/etc/ssl/ca.pem"},
	} {
		_, err := logging.NewForwarder(logging.ForwarderOptions{Kind: logging.ForwardOTLP, OTLP: o})
		if err == nil {
			t.Errorf("%s accepted", name)
		} else if strings.Contains(err.Error(), "192.0.2.10") || strings.Contains(err.Error(), "collector.example.org") {
			t.Errorf("%s: error echoes the endpoint: %v", name, err)
		}
	}
	for _, endpoint := range []string{"http://127.0.0.1:4318", "http://[::1]:4318"} {
		f, err := logging.NewForwarder(logging.ForwarderOptions{Kind: logging.ForwardOTLP, OTLP: logging.OTLPOptions{Endpoint: endpoint, Insecure: true}})
		if err != nil {
			t.Fatalf("%s: %v", endpoint, err)
		}
		_ = f.Close()
	}
}

func TestEnvironmentCannotRedirectExport(t *testing.T) {
	r := newReceiver(t)
	other := newReceiver(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", other.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", other.URL+"/v1/logs")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_HEADERS", "X-Env=leak")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_COMPRESSION", "none")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "host.name=leak")
	t.Setenv("OTEL_SERVICE_NAME", "leak")
	f := newForwarder(t, otlpOptions(r.URL))
	_, _ = f.Write([]byte(sample))
	if err := f.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if other.attempts.Load() != 0 || len(r.requests) != 1 || r.headers[0].Get("X-Env") != "" || r.headers[0].Get("Content-Encoding") != "gzip" {
		t.Fatalf("environment changed the export: other %d, headers %v", other.attempts.Load(), r.headers)
	}
	if bytes.Contains(r.raw[0], []byte("leak")) {
		t.Fatalf("environment changed the records: %v", r.requests[0])
	}
}

// The router's record, with the span of the context, reaches the collector
// with the same identifiers the log file shows.
func TestRouterSpanReachesLogRecord(t *testing.T) {
	r := newReceiver(t)
	router, err := logging.Open(logging.Options{Forward: []logging.ForwarderOptions{otlpOptions(r.URL)}}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	trace, _ := domain.ParseTraceID(traceHex)
	span, _ := domain.NewSpanID()
	ctx := domain.ContextWithSpan(context.Background(), domain.NewRootSpanContext(trace, span, true))
	router.Logger().InfoContext(ctx, "request completed", "component", "http", "status", 204)
	router.Logger().WarnContext(context.Background(), "untraced", "component", "jobs")
	if err := router.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := router.Close(); err != nil {
		t.Fatal(err)
	}
	recs := r.records()
	if len(recs) != 2 {
		t.Fatalf("%d records", len(recs))
	}
	if hex.EncodeToString(recs[0].TraceId) != traceHex || hex.EncodeToString(recs[0].SpanId) != span.String() ||
		attrs(recs[0].Attributes)["status"].GetIntValue() != 204 || recs[0].Body.GetStringValue() != "request completed" {
		t.Fatalf("traced record %v", recs[0])
	}
	if len(recs[1].TraceId) != 0 || recs[1].SeverityNumber != logpb.SeverityNumber_SEVERITY_NUMBER_WARN {
		t.Fatalf("untraced record %v", recs[1])
	}
	for _, st := range router.ForwardStats() {
		if st.Kind != logging.ForwardOTLP || st.Destination != "otlp" || st.Sent != 2 || st.Dropped != 0 {
			t.Fatalf("stats %+v", st)
		}
	}
}

func TestConvertEdgeCases(t *testing.T) {
	for level, want := range map[string]int{"DEBUG": 5, "DEBUG-4": 1, "DEBUG-8": 1, "INFO": 9, "INFO+2": 11, "WARN": 13, "ERROR": 17, "ERROR+8": 24, "TRACE": 9, "INFO+x": 9, "": 9} {
		sev, text := severity(level)
		if int(sev) != want {
			t.Errorf("%q: severity %d, want %d", level, sev, want)
		}
		if want == 9 && level != "INFO" && text != "INFO" {
			t.Errorf("%q: text %q", level, text)
		}
	}
	for _, raw := range []string{"not json", `["a"]`, `{"a":1`, strings.Repeat(`{"a":`, maxDepth+1) + "1" + strings.Repeat("}", maxDepth+1)} {
		if rec := convert([]byte(raw), 1); rec.Body.GetStringValue() != raw || len(rec.Attributes) != 0 || rec.ObservedTimeUnixNano != 1 {
			t.Errorf("%q not sent whole: %v", raw, rec)
		}
	}
	rec := convert([]byte(`{"traceId":"00000000000000000000000000000000","spanId":"0000000000000000","time":"yesterday","level":7}`), 1)
	if len(rec.TraceId) != 0 || len(rec.SpanId) != 0 || rec.TimeUnixNano != 0 || len(rec.Attributes) != 4 {
		t.Fatalf("invalid identifiers, time or level became record fields: %v", rec)
	}
	if rec := convert([]byte(`{"traceId":"`+strings.ToUpper(traceHex)+`"}`), 1); len(rec.TraceId) != 0 {
		t.Fatal("upper case trace ID accepted")
	}
}

// The sender never builds TLS settings or proxies of its own: the client
// from the logging package (tlstrust only, no proxy, no redirects) is the
// only transport.
func TestSenderUsesOnlyTheLoggingClient(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"crypto/tls", "InsecureSkipVerify", "RootCAs", "WithTLSClientConfig", "WithInsecure", "WithProxy", "http.DefaultTransport", "http.DefaultClient"} {
			if strings.Contains(string(data), banned) {
				t.Errorf("%s uses %s", name, banned)
			}
		}
	}
}

func BenchmarkConvert(b *testing.B) {
	raw := []byte(sample)
	b.ReportAllocs()
	for b.Loop() {
		convert(raw, 1)
	}
}
