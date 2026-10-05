package domain

import (
	"context"
	"strings"
	"testing"
)

func TestTraceIdentifiers(t *testing.T) {
	trace, err := NewTraceID()
	if err != nil || !trace.IsValid() {
		t.Fatal("trace id")
	}
	if parsed, ok := ParseTraceID(trace.String()); !ok || parsed != trace {
		t.Fatal("round trip")
	}
	for _, bad := range []string{"", strings.Repeat("0", 32), strings.Repeat("A", 32), strings.Repeat("g", 32), strings.Repeat("a", 31)} {
		if _, ok := ParseTraceID(bad); ok {
			t.Errorf("accepted %q", bad)
		}
	}
	span, err := NewSpanID()
	if err != nil || !span.IsValid() || len(span.String()) != 16 {
		t.Fatal("span id")
	}
}

func TestSpanContextPropagationAndForcing(t *testing.T) {
	if _, ok := SpanFromContext(context.Background()); ok {
		t.Fatal("span without one")
	}
	var nilCtx context.Context
	if _, ok := SpanFromContext(nilCtx); ok {
		t.Fatal("nil context")
	}
	if ContextWithSpan(context.Background(), SpanContext{}) != context.Background() {
		t.Fatal("invalid span stored")
	}
	root := NewRootSpanContext(TraceID{1}, SpanID{1}, false)
	child := root.Child(SpanID{2})
	if child.TraceHex() != root.TraceHex() || child.ParentID() != root.SpanID() || child.SpanHex() == root.SpanHex() || child.Sampled() {
		t.Fatal("child")
	}
	ctx := ContextWithSpan(context.Background(), child)
	ForceTraceSampling(ctx)
	if !root.Sampled() || !root.Forced() || !child.Forced() {
		t.Fatal("forcing must reach every span of the trace")
	}
	orphan := SpanContext{}.Child(SpanID{3})
	if orphan.Forced() || orphan.IsValid() {
		t.Fatal("orphan child")
	}
}
