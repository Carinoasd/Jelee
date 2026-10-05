package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func traceContext(t testing.TB) (context.Context, domain.SpanContext) {
	t.Helper()
	trace, err := domain.NewTraceID()
	if err != nil {
		t.Fatal(err)
	}
	span, err := domain.NewSpanID()
	if err != nil {
		t.Fatal(err)
	}
	sc := domain.NewRootSpanContext(trace, span, false)
	return domain.ContextWithSpan(context.Background(), sc), sc
}

func decodeOne(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &record); err != nil {
		t.Fatalf("record: %v %q", err, buf.String())
	}
	buf.Reset()
	return record
}

// TestRecordsCarryTraceAndSpan (G46.4, G46.6): a record logged with a span
// context carries traceId and spanId, including through derived loggers,
// guarded keys and the console handler; one without a span carries neither.
func TestRecordsCarryTraceAndSpan(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf)
	ctx, sc := traceContext(t)
	logger.InfoContext(ctx, "job started", "component", "jobs")
	record := decodeOne(t, &buf)
	if record["traceId"] != sc.TraceHex() || record["spanId"] != sc.SpanHex() || record["component"] != "jobs" {
		t.Fatalf("trace fields: %v", record)
	}
	logger.With("component", "scan").WarnContext(ctx, "scan slow")
	if record = decodeOne(t, &buf); record["traceId"] != sc.TraceHex() || record["component"] != "scan" {
		t.Fatalf("derived logger: %v", record)
	}
	// The key guard rebuilds the record; trace fields survive it.
	logger.InfoContext(ctx, "guarded", "bad key with spaces", "x")
	if record = decodeOne(t, &buf); record["traceId"] != sc.TraceHex() || record["redactedKey"] == nil {
		t.Fatalf("guarded record: %v", record)
	}
	logger.Info("no context")
	if record = decodeOne(t, &buf); record["traceId"] != nil || record["spanId"] != nil {
		t.Fatalf("record without span: %v", record)
	}
	// An explicit trace attribute wins and is not duplicated.
	other, _ := traceContext(t)
	explicit, _ := domain.SpanFromContext(other)
	logger.InfoContext(ctx, "explicit", "traceId", explicit.TraceHex())
	if strings.Count(buf.String(), `"traceId"`) != 1 {
		t.Fatalf("duplicated trace field: %s", buf.String())
	}
	if record = decodeOne(t, &buf); record["traceId"] != explicit.TraceHex() || record["spanId"] != nil {
		t.Fatalf("explicit trace: %v", record)
	}
	router, err := Open(Options{Format: FormatConsole}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	router.Logger().InfoContext(ctx, "console record", "component", "http")
	if err := router.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), sc.TraceHex()) || !strings.Contains(buf.String(), sc.SpanHex()) {
		t.Fatalf("console lost trace fields: %q", buf.String())
	}
}

// TestTraceFieldWhitelist: only well-formed identifiers and span fields
// pass; anything else in those keys is redacted like unknown data.
func TestTraceFieldWhitelist(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf)
	good := map[string]any{
		"traceId": "0123456789abcdef0123456789abcdef", "linkTraceId": "fedcba9876543210fedcba9876543210",
		"spanId": "0123456789abcdef", "parentSpanId": "fedcba9876543210", "span": "job.inventory_scan",
		"forced": true, "linked": false, "linkKind": "webhook_event", "outcome": "succeeded", "source": "memory_pressure",
		"eventId": "abcDEF_123-x", "deliveryId": "123e4567-e89b-12d3-a456-426614174000", "webhookId": "123e4567-e89b-12d3-a456-426614174000",
		"cpuLimit": int64(4), "ioLimit": int64(8), "totalLimit": int64(16), "percent": int64(75), "pressure": 1.5,
	}
	args := make([]any, 0, 2*len(good))
	for k, v := range good {
		args = append(args, k, v)
	}
	logger.Info("span completed", args...)
	record := decodeOne(t, &buf)
	for k, v := range good {
		want := v
		switch value := v.(type) {
		case int64:
			want = float64(value)
		}
		if record[k] != want {
			t.Errorf("%s: %v, want %v", k, record[k], want)
		}
	}
	bad := []any{"traceId", "0123456789ABCDEF0123456789ABCDEF", "spanId", "short", "parentSpanId", "/etc/passwd/abcdef", "span", "job inventory scan",
		"forced", "yes", "linkKind", "Webhook", "outcome", "C:/private", "source", "postgres://user:secret@h/db", "eventId", "a/b",
		"deliveryId", "not-a-uuid", "cpuLimit", "4", "pressure", "high"}
	logger.Info("span completed", bad...)
	record = decodeOne(t, &buf)
	for i := 0; i < len(bad); i += 2 {
		if key := bad[i].(string); record[key] != "[redacted]" {
			t.Errorf("%s passed with an unsafe value: %v", key, record[key])
		}
	}
}

// BenchmarkLogHotPath compares one access-log record without a span with
// the same record carrying traceId and spanId from the context (G46.8).
func BenchmarkLogHotPath(b *testing.B) {
	logger := New(io.Discard)
	ctx, _ := traceContext(b)
	attrs := []any{"component", "http", "requestId", "0123456789abcdef0123456789abcdef", "method", "GET", "durationMs", int64(3)}
	b.Run("without_span", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			logger.InfoContext(context.Background(), "request completed", attrs...)
		}
	})
	b.Run("with_span", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			logger.InfoContext(ctx, "request completed", attrs...)
		}
	})
	b.Run("filtered_with_span", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			logger.Log(ctx, slog.LevelDebug, "request completed", attrs...)
		}
	})
}
