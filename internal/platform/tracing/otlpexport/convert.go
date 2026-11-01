package otlpexport

import (
	"log/slog"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/buildinfo"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
	"github.com/MoYuanCN/Jelee/internal/platform/tracing"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// ScopeName is the instrumentation scope of every exported span.
const ScopeName = "github.com/MoYuanCN/Jelee/internal/platform/tracing"

// AttributePrefix namespaces the exported span attributes.
const AttributePrefix = "jelee."

// spanAttributes are the keys of a "span completed" record that become span
// attributes. The identifiers and the parent become the span's own fields,
// durationMs its start and end times; any other key is never exported. Each
// value still passes the log whitelist (logging.SpanAttr), so an exported
// attribute is never more than its log record shows.
var spanAttributes = map[string]bool{"component": true, "forced": true, "linkKind": true, "linked": true, "outcome": true}

// errorOutcomes mark a span as failed: a request that panicked, a job that
// failed or lost its lease, a webhook attempt that will be retried (pending)
// or went to the dead letter state.
var errorOutcomes = map[string]bool{"panic": true, "failed": true, "lease_lost": true, "dead": true}

// converter turns span records into the SDK's read-only spans. It is built
// once: the resource names only the service and its version, never the
// OTEL_RESOURCE_ATTRIBUTES or host details of the environment.
type converter struct {
	resource *resource.Resource
	scope    instrumentation.Scope
}

func newConverter() converter {
	return converter{
		resource: resource.NewSchemaless(attribute.String("service.name", "jelee"), attribute.String("service.version", buildinfo.Version())),
		scope:    instrumentation.Scope{Name: ScopeName, Version: buildinfo.Version()},
	}
}

// spanContext keeps the tracer's identifiers: the exported trace and span
// IDs are the traceId and spanId of the log records.
func spanContext(sc domain.SpanContext, remote bool) trace.SpanContext {
	if !sc.IsValid() {
		return trace.SpanContext{}
	}
	return trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID(sc.TraceID()), SpanID: trace.SpanID(sc.SpanID()), TraceFlags: trace.FlagsSampled, Remote: remote})
}

func spanKind(name string) trace.SpanKind {
	switch {
	case name == "http.request":
		return trace.SpanKindServer
	case name == "webhook.deliver":
		return trace.SpanKindClient
	case strings.HasPrefix(name, "job."):
		return trace.SpanKindConsumer
	}
	return trace.SpanKindInternal
}

func (c converter) span(r tracing.SpanRecord) sdktrace.ReadOnlySpan {
	name := r.Name
	// Span names are constants of the call sites; anything else is
	// refused like the log field "span" would be.
	if v := logging.SpanAttr(slog.String("span", name)); v.Value.String() != name {
		name = "redacted"
	}
	stub := tracetest.SpanStub{
		Name:                 name,
		SpanContext:          spanContext(r.Context, false),
		SpanKind:             spanKind(name),
		StartTime:            r.Start,
		EndTime:              r.End,
		Resource:             c.resource,
		InstrumentationScope: c.scope,
	}
	if parent := r.Context.ParentID(); parent.IsValid() {
		stub.Parent = trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID(r.Context.TraceID()), SpanID: trace.SpanID(parent), TraceFlags: trace.FlagsSampled})
	}
	outcome := ""
	attrs := make([]attribute.KeyValue, 0, len(spanAttributes))
	for _, a := range r.Attrs {
		if !spanAttributes[a.Key] {
			continue
		}
		a = logging.SpanAttr(a)
		key := AttributePrefix + a.Key
		switch a.Value.Kind() {
		case slog.KindBool:
			attrs = append(attrs, attribute.Bool(key, a.Value.Bool()))
		default:
			attrs = append(attrs, attribute.String(key, a.Value.String()))
		}
		if a.Key == "outcome" {
			outcome = a.Value.String()
		}
	}
	stub.Attributes = attrs
	if errorOutcomes[outcome] || name == "webhook.deliver" && outcome == "pending" {
		stub.Status = sdktrace.Status{Code: codes.Error, Description: outcome}
	}
	if link := spanContext(r.Link, true); link.IsValid() {
		kind := ""
		for _, a := range r.Attrs {
			if a.Key == "linkKind" {
				kind = logging.SpanAttr(a).Value.String()
			}
		}
		stub.Links = []sdktrace.Link{{SpanContext: link, Attributes: []attribute.KeyValue{attribute.String(AttributePrefix+"linkKind", kind)}}}
	}
	return stub.Snapshot()
}
