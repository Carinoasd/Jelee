package tracing

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
)

func newTestTracer(t *testing.T, rate float64, random float64) (*Tracer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	now := time.Unix(1000, 0)
	tracer, err := New(Options{SampleRate: rate, Logger: logging.New(&buf), Links: NewLinks(8),
		Now: func() time.Time { now = now.Add(5 * time.Millisecond); return now }, Random: func() float64 { return random }})
	if err != nil {
		t.Fatal(err)
	}
	return tracer, &buf
}

func spanRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		out = append(out, record)
	}
	return out
}

func TestNewRejectsInvalidRates(t *testing.T) {
	for _, rate := range []float64{-0.1, 1.01} {
		if _, err := New(Options{SampleRate: rate}); err == nil {
			t.Fatalf("rate %v accepted", rate)
		}
	}
	if ValidSampleRate(0) != true || ValidSampleRate(1) != true {
		t.Fatal("bounds rejected")
	}
}

func TestRequestSpanUsesRequestIDAndChildrenShareTrace(t *testing.T) {
	tracer, buf := newTestTracer(t, 1, 0.5)
	const requestID = "0123456789abcdef0123456789abcdef"
	ctx, root := tracer.StartRequest(context.Background(), requestID, "http.request", "http")
	sc, ok := domain.SpanFromContext(ctx)
	if !ok || sc.TraceHex() != requestID || root.Context().SpanHex() != sc.SpanHex() || !sc.Sampled() {
		t.Fatalf("request span: %+v", sc)
	}
	child, span := tracer.Start(ctx, "media.direct", "media")
	csc, _ := domain.SpanFromContext(child)
	if csc.TraceHex() != requestID || csc.ParentID() != sc.SpanID() || csc.SpanID() == sc.SpanID() {
		t.Fatal("child span left the trace")
	}
	span.End("ok")
	span.End("again")
	root.End("ok")
	records := spanRecords(t, buf)
	if len(records) != 2 {
		t.Fatalf("span records: %d", len(records))
	}
	first := records[0]
	if first["msg"] != "span completed" || first["span"] != "media.direct" || first["component"] != "media" || first["traceId"] != requestID ||
		first["parentSpanId"] != sc.SpanHex() || first["outcome"] != "ok" || first["durationMs"] != float64(5) {
		t.Fatalf("child record: %v", first)
	}
	// A malformed request ID starts a fresh random trace instead.
	ctx, _ = tracer.StartRequest(context.Background(), "not-hex", "http.request", "http")
	if sc, _ := domain.SpanFromContext(ctx); !sc.IsValid() || sc.TraceHex() == "not-hex" {
		t.Fatal("malformed request ID not replaced")
	}
}

// TestSamplingDropsOnlySpanRecordsAndSecurityForces: an unsampled trace
// still propagates identifiers, writes no span record, and a security event
// anywhere in it (including a child span) forces every later span record.
func TestSamplingDropsOnlySpanRecordsAndSecurityForces(t *testing.T) {
	tracer, buf := newTestTracer(t, 0.25, 0.9)
	ctx, root := tracer.StartRequest(context.Background(), "", "http.request", "http")
	if sc, ok := domain.SpanFromContext(ctx); !ok || sc.Sampled() {
		t.Fatal("head sampling ignored the rate")
	}
	_, quiet := tracer.Start(ctx, "media.direct", "media")
	quiet.End("ok")
	if buf.Len() != 0 {
		t.Fatal("unsampled span wrote a record")
	}
	child, login := tracer.Start(ctx, "auth.login", "auth")
	domain.ForceTraceSampling(child)
	login.End("denied")
	root.End("ok")
	records := spanRecords(t, buf)
	if len(records) != 2 || records[0]["forced"] != true || records[1]["span"] != "http.request" || records[1]["forced"] != true {
		t.Fatalf("forced trace records: %v", records)
	}
	// Rate 1 always samples, rate 0 never does (without a security event).
	always, _ := newTestTracer(t, 1, 0.99)
	never, _ := newTestTracer(t, 0, 0)
	if c, _ := always.Start(context.Background(), "x", "jobs"); !mustSpan(c, t).Sampled() {
		t.Fatal("rate 1 dropped a trace")
	}
	if c, _ := never.Start(context.Background(), "x", "jobs"); mustSpan(c, t).Sampled() {
		t.Fatal("rate 0 kept a trace")
	}
	// ForceTraceSampling without a span is harmless.
	domain.ForceTraceSampling(context.Background())
}

func mustSpan(ctx context.Context, t *testing.T) domain.SpanContext {
	t.Helper()
	sc, ok := domain.SpanFromContext(ctx)
	if !ok {
		t.Fatal("no span in context")
	}
	return sc
}

// TestLinkedWorkJoinsSubmitterTrace: work remembered by its submitter joins
// that trace; unknown work starts a linked root.
func TestLinkedWorkJoinsSubmitterTrace(t *testing.T) {
	tracer, buf := newTestTracer(t, 1, 0)
	request, root := tracer.StartRequest(context.Background(), "", "http.request", "http")
	tracer.Remember(request, LinkJob, "job-1")
	tracer.Remember(context.Background(), LinkJob, "job-2") // no span: nothing remembered
	root.End("ok")
	submitted := mustSpan(request, t)
	work, span := tracer.StartLinked(context.Background(), "job.inventory_scan", "scan", LinkJob, "job-1")
	joined := mustSpan(work, t)
	if joined.TraceID() != submitted.TraceID() || joined.ParentID() != submitted.SpanID() {
		t.Fatal("claimed job did not join the submitting trace")
	}
	span.End("succeeded")
	other, orphan := tracer.StartLinked(context.Background(), "webhook.deliver", "webhook", LinkWebhookEvent, "event-9")
	if sc := mustSpan(other, t); sc.TraceID() == submitted.TraceID() || sc.ParentID().IsValid() {
		t.Fatal("unknown work joined a foreign trace")
	}
	orphan.End("")
	records := spanRecords(t, buf)
	if len(records) != 3 || records[1]["linked"] != true || records[1]["linkKind"] != LinkJob || records[2]["linked"] != false || records[2]["linkKind"] != LinkWebhookEvent {
		t.Fatalf("linked span records: %v", records)
	}
}

func TestLinksAreBoundedAndReplace(t *testing.T) {
	links := NewLinks(2)
	sc := domain.NewRootSpanContext(domain.TraceID{1}, domain.SpanID{1}, true)
	links.Put("job", "a", sc)
	links.Put("job", "b", sc)
	links.Put("job", "a", sc.Child(domain.SpanID{2}))
	if got, _ := links.Lookup("job", "a"); got.SpanID() != (domain.SpanID{2}) || links.Len() != 2 {
		t.Fatal("replacement lost")
	}
	links.Put("job", "c", sc)
	if _, ok := links.Lookup("job", "a"); ok || links.Len() != 2 {
		t.Fatal("oldest entry not evicted")
	}
	for _, bad := range []struct{ kind, id string }{{"", "x"}, {"job", ""}, {"job", strings.Repeat("x", 129)}} {
		links.Put(bad.kind, bad.id, sc)
	}
	links.Put("job", "d", domain.SpanContext{})
	if _, ok := links.Lookup("job", "d"); ok || links.Len() != 2 {
		t.Fatal("invalid entries remembered")
	}
	if NewLinks(0).limit != 1 {
		t.Fatal("limit floor")
	}
	var none *Links
	if _, ok := none.Lookup("job", "a"); ok {
		t.Fatal("nil registry found an entry")
	}
}

// TestNilTracerPropagatesWithoutRecords: before a tracer is installed every
// path still gets identifiers, so logs correlate even without span records.
func TestNilTracerPropagatesWithoutRecords(t *testing.T) {
	var tracer *Tracer
	ctx, span := tracer.StartRequest(context.Background(), "0123456789abcdef0123456789abcdef", "http.request", "http")
	if mustSpan(ctx, t).TraceHex() != "0123456789abcdef0123456789abcdef" {
		t.Fatal("nil tracer lost the request trace")
	}
	tracer.Remember(ctx, LinkJob, "nil-job")
	work, _ := tracer.StartLinked(context.Background(), "job.x", "jobs", LinkJob, "nil-job")
	if mustSpan(work, t).TraceID() != mustSpan(ctx, t).TraceID() {
		t.Fatal("nil tracer did not use the default link registry")
	}
	span.End("ok")
	var nothing *Span
	nothing.End("ok")
	if nothing.Context().IsValid() {
		t.Fatal("nil span has a context")
	}
	previous := SetDefault(nil)
	defer SetDefault(previous)
	if Default() != nil {
		t.Fatal("default not replaced")
	}
	Remember(ctx, LinkJob, "nil-job-2")
	if _, ok := DefaultLinks().Lookup(LinkJob, "nil-job-2"); !ok {
		t.Fatal("package Remember lost the entry")
	}
}

// BenchmarkRequestSpan is the per-request cost of the HTTP root span when
// the trace is not sampled (the common case) and when it is.
func BenchmarkRequestSpan(b *testing.B) {
	for _, rate := range []float64{0, 1} {
		tracer, err := New(Options{SampleRate: rate, Logger: logging.New(io.Discard), Links: NewLinks(8)})
		if err != nil {
			b.Fatal(err)
		}
		name := "unsampled"
		if rate == 1 {
			name = "sampled"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, span := tracer.StartRequest(context.Background(), "0123456789abcdef0123456789abcdef", "http.request", "http")
				span.End("ok")
			}
		})
	}
}
