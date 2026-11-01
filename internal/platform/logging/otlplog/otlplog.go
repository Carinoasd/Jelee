// Package otlplog is the sender of the otlp log forwarder kind (G46.1): it
// turns the router's redacted JSON records into OpenTelemetry log records and
// pushes them to a collector over OTLP/HTTP, protobuf encoded and gzip
// compressed, at <endpoint>/v1/logs. Linking the package registers the
// sender with the logging package; only the server links it, so the command
// line tools stay free of the protobuf modules.
//
// Queueing, batching, retry with backoff (and Retry-After), the shutdown
// deadline and the drop counters stay in the logging forwarder core, shared
// with syslog and Loki: one batch is one request, and its response status
// decides whether the core retries.
//
// The request is built from the OTLP protobuf definitions directly instead
// of the OpenTelemetry log SDK: the SDK's logger provider always merges
// OTEL_RESOURCE_ATTRIBUTES and OTEL_SERVICE_NAME into the resource, and the
// Resource here, as for the trace export, is exactly service.name and
// service.version. No OTEL_* variable changes what is sent or where.
package otlplog

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"strconv"

	"github.com/MoYuanCN/Jelee/internal/platform/buildinfo"
	"github.com/MoYuanCN/Jelee/internal/platform/logging"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logpb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
)

// ScopeName is the instrumentation scope of every forwarded record.
const ScopeName = "github.com/MoYuanCN/Jelee/internal/platform/logging"

// maxResponseBytes is how much of a response body is read before the
// connection is reused; the body is never kept.
const maxResponseBytes = 4096

func init() { logging.RegisterOTLPSender(New) }

// sender is used from the forwarder worker goroutine only.
type sender struct {
	url      string
	header   http.Header
	client   *http.Client
	resource *resourcepb.Resource
	scope    *commonpb.InstrumentationScope
	buf      bytes.Buffer
	zw       *gzip.Writer
	now      func() int64
}

// New builds the OTLP/HTTP sender for t. It sends with t's client only.
func New(t logging.OTLPTarget) (logging.ForwardSender, error) {
	header := http.Header{}
	for name, value := range t.Headers {
		header.Set(name, value)
	}
	header.Set("Content-Type", "application/x-protobuf")
	header.Set("Content-Encoding", "gzip")
	header.Set("User-Agent", "jelee-log-forwarder")
	s := &sender{
		url: t.URL, header: header, client: t.Client,
		resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
			{Key: "service.name", Value: stringValue("jelee")},
			{Key: "service.version", Value: stringValue(buildinfo.Version())},
		}},
		scope: &commonpb.InstrumentationScope{Name: ScopeName, Version: buildinfo.Version()},
		now:   unixNano,
	}
	s.zw = gzip.NewWriter(&s.buf)
	return s, nil
}

// body encodes one batch as an ExportLogsServiceRequest. LogsData has the
// same single field (resource_logs = 1), so its encoding is the request's
// and the collector package with its gRPC stubs is not needed.
func (s *sender) body(records [][]byte) ([]byte, error) {
	observed := s.now()
	logs := make([]*logpb.LogRecord, len(records))
	for i, raw := range records {
		logs[i] = convert(raw, observed)
	}
	data, err := proto.Marshal(&logpb.LogsData{ResourceLogs: []*logpb.ResourceLogs{{
		Resource:  s.resource,
		ScopeLogs: []*logpb.ScopeLogs{{Scope: s.scope, LogRecords: logs}},
	}}})
	if err != nil {
		return nil, err
	}
	s.buf.Reset()
	s.zw.Reset(&s.buf)
	if _, err := s.zw.Write(data); err != nil {
		return nil, err
	}
	if err := s.zw.Close(); err != nil {
		return nil, err
	}
	return s.buf.Bytes(), nil
}

// Send pushes the batch in one request.
func (s *sender) Send(ctx context.Context, records [][]byte) error {
	payload, err := s.body(records)
	if err != nil {
		return logging.ForwardFailure("encode", false, 0)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(payload))
	if err != nil {
		return logging.ForwardFailure("request", false, 0)
	}
	req.Header = s.header.Clone()
	resp, err := s.client.Do(req)
	if err != nil {
		return logging.ForwardNetworkFailure(err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
	_ = resp.Body.Close()
	return classify(resp.StatusCode, resp.Header.Get("Retry-After"))
}

// classify maps the response status to the forwarder core's retry policy;
// the response body (which can quote the request) is never kept.
func classify(code int, retryAfter string) error {
	switch {
	case code >= 200 && code < 300:
		// Accepted; a partial success still delivered the batch.
		return nil
	case code == http.StatusTooManyRequests || code == http.StatusRequestTimeout || code >= 500:
		return logging.ForwardFailure("status "+strconv.Itoa(code), true, logging.ForwardRetryAfter(retryAfter))
	default:
		// 400 (malformed), 401/403 (credentials), 413 and redirects do not
		// change on retry.
		return logging.ForwardFailure("rejected "+strconv.Itoa(code), false, 0)
	}
}

// Close releases the client's kept-alive connections.
func (s *sender) Close() { s.client.CloseIdleConnections() }
