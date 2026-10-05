package domain

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync/atomic"
)

// ErrTraceRandom reports that the system random source failed.
var ErrTraceRandom = errors.New("trace identifier unavailable")

// TraceID identifies one trace (W3C trace-id width). It is random; the zero
// value is invalid.
type TraceID [16]byte

// SpanID identifies one span inside a trace; the zero value is invalid.
type SpanID [8]byte

// IsValid reports a non-zero identifier.
func (t TraceID) IsValid() bool { return t != TraceID{} }

// IsValid reports a non-zero identifier.
func (s SpanID) IsValid() bool { return s != SpanID{} }

// String returns 32 lowercase hexadecimal digits.
func (t TraceID) String() string { return hex.EncodeToString(t[:]) }

// String returns 16 lowercase hexadecimal digits.
func (s SpanID) String() string { return hex.EncodeToString(s[:]) }

// ParseTraceID accepts exactly 32 lowercase hexadecimal digits that are not
// all zero, the form X-Request-ID and log records use.
func ParseTraceID(value string) (TraceID, bool) {
	var t TraceID
	if len(value) != 32 || !lowerHex(value) {
		return t, false
	}
	if _, err := hex.Decode(t[:], []byte(value)); err != nil {
		return TraceID{}, false
	}
	return t, t.IsValid()
}

func lowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// NewTraceID draws a trace identifier from the system random source.
func NewTraceID() (TraceID, error) {
	var t TraceID
	if _, err := rand.Read(t[:]); err != nil || !t.IsValid() {
		return TraceID{}, ErrTraceRandom
	}
	return t, nil
}

// NewSpanID draws a span identifier from the system random source.
func NewSpanID() (SpanID, error) {
	var s SpanID
	if _, err := rand.Read(s[:]); err != nil || !s.IsValid() {
		return SpanID{}, ErrTraceRandom
	}
	return s, nil
}

// traceState is shared by every span of one trace inside this process, so a
// security event anywhere in the trace forces the whole trace to be kept.
type traceState struct{ forced atomic.Bool }

// SpanContext is the propagated identity of the current span (G46.6). The
// hexadecimal forms are computed once because every log record carries them.
type SpanContext struct {
	traceID  TraceID
	spanID   SpanID
	parentID SpanID
	sampled  bool
	traceHex string
	spanHex  string
	state    *traceState
}

// NewRootSpanContext starts a trace. sampled is the head sampling decision.
func NewRootSpanContext(trace TraceID, span SpanID, sampled bool) SpanContext {
	return SpanContext{traceID: trace, spanID: span, sampled: sampled, traceHex: trace.String(), spanHex: span.String(), state: &traceState{}}
}

// Child returns the context of a new span below sc in the same trace. It
// inherits the sampling decision and the shared forced flag.
func (sc SpanContext) Child(span SpanID) SpanContext {
	state := sc.state
	if state == nil {
		state = &traceState{}
	}
	return SpanContext{traceID: sc.traceID, spanID: span, parentID: sc.spanID, sampled: sc.sampled, traceHex: sc.traceHex, spanHex: span.String(), state: state}
}

// IsValid reports a context with both identifiers set.
func (sc SpanContext) IsValid() bool { return sc.traceID.IsValid() && sc.spanID.IsValid() }

// TraceID returns the trace identifier.
func (sc SpanContext) TraceID() TraceID { return sc.traceID }

// SpanID returns the span identifier.
func (sc SpanContext) SpanID() SpanID { return sc.spanID }

// ParentID returns the parent span, zero for a root span.
func (sc SpanContext) ParentID() SpanID { return sc.parentID }

// TraceHex returns the log form of the trace identifier (empty when invalid).
func (sc SpanContext) TraceHex() string { return sc.traceHex }

// SpanHex returns the log form of the span identifier (empty when invalid).
func (sc SpanContext) SpanHex() string { return sc.spanHex }

// Sampled reports whether span records of this trace are kept: by the head
// decision or because a security event forced it.
func (sc SpanContext) Sampled() bool { return sc.sampled || sc.Forced() }

// Forced reports whether a security event in this trace forced sampling.
func (sc SpanContext) Forced() bool { return sc.state != nil && sc.state.forced.Load() }

type spanContextKey struct{}

// ContextWithSpan stores sc as the current span. An invalid sc leaves ctx
// unchanged.
func ContextWithSpan(ctx context.Context, sc SpanContext) context.Context {
	if !sc.IsValid() {
		return ctx
	}
	return context.WithValue(ctx, spanContextKey{}, sc)
}

// SpanFromContext returns the current span, if any.
func SpanFromContext(ctx context.Context) (SpanContext, bool) {
	if ctx == nil {
		return SpanContext{}, false
	}
	sc, ok := ctx.Value(spanContextKey{}).(SpanContext)
	return sc, ok && sc.IsValid()
}

// ForceTraceSampling marks the current trace as kept regardless of the head
// sampling decision. Security events (audit security rows, failed logins,
// blocked clients) call it so their trace is never dropped by sampling. It
// is a no-op without a current span.
func ForceTraceSampling(ctx context.Context) {
	if sc, ok := SpanFromContext(ctx); ok && sc.state != nil {
		sc.state.forced.Store(true)
	}
}
