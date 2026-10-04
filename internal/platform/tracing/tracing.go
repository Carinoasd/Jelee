// Package tracing correlates logs along the key paths of G46.6: every span
// puts its trace and span identifiers into the context, the logging handler
// writes them into each record logged with that context, and sampled spans
// emit one "span completed" record when they end. Jelee ships no trace
// exporter, so logs are the trace store; span records are the only output
// that sampling drops, and security events force their trace to be kept.
package tracing

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// DefaultSampleRate keeps one trace in ten.
const DefaultSampleRate = 0.1

// Link kinds of background work.
const (
	LinkJob          = "job"
	LinkWebhookEvent = "webhook_event"
)

// Options configures a Tracer. A nil Logger discards span records; Now and
// Random exist for tests.
type Options struct {
	SampleRate float64
	Logger     *slog.Logger
	Now        func() time.Time
	Random     func() float64
	Links      *Links
}

// Tracer starts spans. A nil *Tracer is usable: it still creates identifiers
// (so logs correlate) but never writes span records.
type Tracer struct {
	rate   float64
	logger *slog.Logger
	now    func() time.Time
	random func() float64
	links  *Links
}

// ValidSampleRate reports whether rate is a probability.
func ValidSampleRate(rate float64) bool {
	return !math.IsNaN(rate) && rate >= 0 && rate <= 1
}

// New validates o and returns a Tracer.
func New(o Options) (*Tracer, error) {
	if !ValidSampleRate(o.SampleRate) {
		return nil, errors.New("trace sample rate must be between 0 and 1")
	}
	t := &Tracer{rate: o.SampleRate, logger: o.Logger, now: o.Now, random: o.Random, links: o.Links}
	if t.logger == nil {
		t.logger = slog.New(slog.DiscardHandler)
	}
	if t.now == nil {
		t.now = time.Now
	}
	if t.random == nil {
		t.random = rand.Float64
	}
	if t.links == nil {
		t.links = DefaultLinks()
	}
	return t, nil
}

var defaultTracer atomic.Pointer[Tracer]

// Default returns the process tracer installed by SetDefault; before that it
// returns nil, which still propagates identifiers.
func Default() *Tracer { return defaultTracer.Load() }

// SetDefault installs the process tracer and returns the previous one.
func SetDefault(t *Tracer) *Tracer { return defaultTracer.Swap(t) }

func (t *Tracer) sample() bool {
	if t == nil {
		return false
	}
	switch {
	case t.rate >= 1:
		return true
	case t.rate <= 0:
		return false
	}
	return t.random() < t.rate
}

func (t *Tracer) linkRegistry() *Links {
	if t == nil {
		return DefaultLinks()
	}
	return t.links
}

// Span is one started span. End is idempotent and safe on a nil Span.
type Span struct {
	// ctx is the context the span was started into; the span record is
	// logged with it so it carries the span's own identifiers.
	ctx       context.Context
	tracer    *Tracer
	sc        domain.SpanContext
	name      string
	component string
	start     time.Time
	link      domain.SpanContext
	linkKind  string
	once      sync.Once
}

// Context returns the span identity.
func (s *Span) Context() domain.SpanContext {
	if s == nil {
		return domain.SpanContext{}
	}
	return s.sc
}

func (t *Tracer) begin(ctx context.Context, sc domain.SpanContext, name, component string) (context.Context, *Span) {
	ctx = domain.ContextWithSpan(ctx, sc)
	s := &Span{ctx: ctx, tracer: t, sc: sc, name: name, component: component}
	if t != nil {
		s.start = t.now()
	}
	return ctx, s
}

func (t *Tracer) root(ctx context.Context, trace domain.TraceID, name, component string) (context.Context, *Span) {
	span, err := domain.NewSpanID()
	if err != nil {
		return ctx, nil
	}
	return t.begin(ctx, domain.NewRootSpanContext(trace, span, t.sample()), name, component)
}

// StartRequest starts the root span of one HTTP request. A request ID of 32
// lowercase hex digits becomes the trace ID, so X-Request-ID, the error
// envelope traceId, audit request_id and log traceId are the same value.
// Inbound traceparent headers are not trusted: a client must not choose the
// identifiers that correlate security records.
func (t *Tracer) StartRequest(ctx context.Context, requestID, name, component string) (context.Context, *Span) {
	trace, ok := domain.ParseTraceID(requestID)
	if !ok {
		var err error
		if trace, err = domain.NewTraceID(); err != nil {
			return ctx, nil
		}
	}
	return t.root(ctx, trace, name, component)
}

// Start starts a child of the current span, or a new root without one.
func (t *Tracer) Start(ctx context.Context, name, component string) (context.Context, *Span) {
	if parent, ok := domain.SpanFromContext(ctx); ok {
		span, err := domain.NewSpanID()
		if err != nil {
			return ctx, nil
		}
		return t.begin(ctx, parent.Child(span), name, component)
	}
	trace, err := domain.NewTraceID()
	if err != nil {
		return ctx, nil
	}
	return t.root(ctx, trace, name, component)
}

// StartLinked starts the span of background work (a claimed job or a webhook
// delivery). When the submitting span was remembered under (kind, id) in
// this process, the work continues that trace as its child; otherwise it
// starts a new root and its span record names the work it belongs to, so
// the submitting request and the work are joined by taskId or eventId.
func (t *Tracer) StartLinked(ctx context.Context, name, component, kind, id string) (context.Context, *Span) {
	if submitted, ok := t.linkRegistry().Lookup(kind, id); ok {
		span, err := domain.NewSpanID()
		if err != nil {
			return ctx, nil
		}
		ctx, s := t.begin(ctx, submitted.Child(span), name, component)
		s.link, s.linkKind = submitted, kind
		return ctx, s
	}
	trace, err := domain.NewTraceID()
	if err != nil {
		return ctx, nil
	}
	ctx, s := t.root(ctx, trace, name, component)
	if s != nil {
		s.linkKind = kind
	}
	return ctx, s
}

// Remember records the current span as the submitter of (kind, id).
func (t *Tracer) Remember(ctx context.Context, kind, id string) {
	if sc, ok := domain.SpanFromContext(ctx); ok {
		t.linkRegistry().Put(kind, id, sc)
	}
}

// Remember records the current span in the process link registry.
func Remember(ctx context.Context, kind, id string) { Default().Remember(ctx, kind, id) }

// End writes the span record when the trace is sampled or forced. outcome is
// a short lower-case word such as ok, failed or cancelled (empty for none).
func (s *Span) End(outcome string) {
	if s == nil || s.tracer == nil {
		return
	}
	s.once.Do(func() {
		if !s.sc.Sampled() {
			return
		}
		attrs := make([]slog.Attr, 0, 8)
		attrs = append(attrs, slog.String("component", s.component), slog.String("span", s.name),
			slog.Int64("durationMs", s.tracer.now().Sub(s.start).Milliseconds()), slog.Bool("forced", s.sc.Forced()))
		if parent := s.sc.ParentID(); parent.IsValid() {
			attrs = append(attrs, slog.String("parentSpanId", parent.String()))
		}
		if s.linkKind != "" {
			attrs = append(attrs, slog.String("linkKind", s.linkKind), slog.Bool("linked", s.link.IsValid()))
		}
		if outcome != "" {
			attrs = append(attrs, slog.String("outcome", outcome))
		}
		s.tracer.logger.LogAttrs(s.ctx, slog.LevelInfo, "span completed", attrs...)
	})
}
