package media

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
	"github.com/MoYuanCN/Jelee/internal/platform/tracing"
)

// TestDirectDeliverySpan (G46.6): direct delivery runs in a child span of
// the request, visible to the resolver, and its span record stays in the
// request trace.
func TestDirectDeliverySpan(t *testing.T) {
	var logs bytes.Buffer
	tracer, err := tracing.New(tracing.Options{SampleRate: 1, Logger: logging.New(&logs), Links: tracing.NewLinks(4)})
	if err != nil {
		t.Fatal(err)
	}
	previous := tracing.SetDefault(tracer)
	defer tracing.SetDefault(previous)
	source, _ := fixture(t)
	const requestID = "00112233445566778899aabbccddeeff"
	var seen domain.SpanContext
	handler := testHandler(t, resolveFunc(func(ctx context.Context, _ access.Principal, _ string) (Source, error) {
		seen, _ = domain.SpanFromContext(ctx)
		return source, nil
	}), 1)
	r := nativeRequest("HEAD", "/stream")
	ctx, root := tracer.StartRequest(r.Context(), requestID, "http.request", "http")
	w := httptest.NewRecorder()
	handler.ServeSource(w, r.WithContext(ctx), "source")
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	if seen.TraceHex() != requestID || seen.ParentID() != root.Context().SpanID() {
		t.Fatalf("resolver span: trace %s parent %s", seen.TraceHex(), seen.ParentID())
	}
	out := logs.String()
	if !strings.Contains(out, `"span":"media.direct"`) || !strings.Contains(out, `"traceId":"`+requestID+`"`) || !strings.Contains(out, `"parentSpanId":"`+root.Context().SpanHex()+`"`) {
		t.Fatalf("direct span record: %s", out)
	}
}
