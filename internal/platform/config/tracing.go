package config

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/tracing"
)

// TracingConfig selects the optional span exporter (G46.6, principle 5).
// Sampling stays logging.traceSampleRate: the exporter receives exactly the
// spans whose "span completed" record is logged.
type TracingConfig struct {
	// Exporter is none (default, also when empty) or otlp.
	Exporter string          `json:"exporter"`
	OTLP     OTLPTraceConfig `json:"otlp"`
}

// OTLPTraceConfig configures OTLP/HTTP export. Zero numbers select the
// defaults.
type OTLPTraceConfig struct {
	// Endpoint is the collector's absolute URL; without a path the traces
	// path /v1/traces is used.
	Endpoint string `json:"endpoint"`
	// Headers are sent with every export (authentication, tenant). They
	// are secrets: never logged or echoed in errors.
	Headers map[string]string `json:"headers"`
	// Insecure allows plain http, only to a loopback IP literal.
	Insecure bool `json:"insecure"`
	// CAFile is a PEM file of one or more CA certificates trusted instead of
	// the system roots for an https endpoint. Certificates are always
	// verified.
	CAFile          string `json:"caFile"`
	TimeoutSeconds  int    `json:"timeoutSeconds"`
	BatchSize       int    `json:"batchSize"`
	BatchIntervalMs int    `json:"batchIntervalMs"`
	QueueSize       int    `json:"queueSize"`
}

// Tracing exporter kinds.
const (
	TraceExporterNone = "none"
	TraceExporterOTLP = "otlp"
)

// Enabled reports whether spans are exported.
func (c TracingConfig) Enabled() bool { return c.Exporter == TraceExporterOTLP }

func (c OTLPTraceConfig) set() bool {
	return c.Endpoint != "" || len(c.Headers) > 0 || c.Insecure || c.CAFile != "" || c.TimeoutSeconds != 0 || c.BatchSize != 0 || c.BatchIntervalMs != 0 || c.QueueSize != 0
}

// Validate refuses a half configured exporter. It never includes the
// endpoint or header values in its errors.
func (c TracingConfig) Validate() error {
	switch c.Exporter {
	case "", TraceExporterNone:
		if c.OTLP.set() {
			return errors.New("tracing otlp settings require tracing.exporter otlp")
		}
		return nil
	case TraceExporterOTLP:
	default:
		return errors.New("tracing exporter must be none or otlp")
	}
	if c.OTLP.Endpoint == "" {
		return errors.New("tracing exporter otlp requires tracing.otlp.endpoint")
	}
	return c.OTLP.Settings().Validate()
}

// Settings converts the configuration into exporter settings.
func (c OTLPTraceConfig) Settings() tracing.OTLPSettings {
	return tracing.OTLPSettings{Endpoint: c.Endpoint, Headers: c.Headers, Insecure: c.Insecure, CAFile: c.CAFile,
		Timeout: time.Duration(c.TimeoutSeconds) * time.Second, BatchSize: c.BatchSize,
		Interval: time.Duration(c.BatchIntervalMs) * time.Millisecond, QueueSize: c.QueueSize}
}

// loadEnvironment reads JELEE_TRACE_EXPORTER and JELEE_TRACE_OTLP_*.
// JELEE_TRACE_OTLP_HEADERS is "name=value,name=value" and replaces the
// file's headers; values are URL-decoded like OTEL_EXPORTER_OTLP_HEADERS.
func (c *TracingConfig) loadEnvironment(lookup func(string) (string, bool)) error {
	if value, ok := lookup("JELEE_TRACE_EXPORTER"); ok {
		c.Exporter = value
	}
	if value, ok := lookup("JELEE_TRACE_OTLP_ENDPOINT"); ok {
		c.OTLP.Endpoint = value
	}
	if value, ok := lookup("JELEE_TRACE_OTLP_CA_FILE"); ok {
		c.OTLP.CAFile = value
	}
	if value, ok := lookup("JELEE_TRACE_OTLP_INSECURE"); ok {
		b, err := strconv.ParseBool(value)
		if err != nil {
			return errors.New("invalid JELEE_TRACE_OTLP_INSECURE")
		}
		c.OTLP.Insecure = b
	}
	for name, target := range map[string]*int{"JELEE_TRACE_OTLP_TIMEOUT_SECONDS": &c.OTLP.TimeoutSeconds, "JELEE_TRACE_OTLP_BATCH_SIZE": &c.OTLP.BatchSize,
		"JELEE_TRACE_OTLP_BATCH_INTERVAL_MS": &c.OTLP.BatchIntervalMs, "JELEE_TRACE_OTLP_QUEUE_SIZE": &c.OTLP.QueueSize} {
		if value, ok := lookup(name); ok {
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid %s", name)
			}
			*target = n
		}
	}
	if value, ok := lookup("JELEE_TRACE_OTLP_HEADERS"); ok {
		headers, err := parseHeaderList(value)
		if err != nil {
			return errors.New("invalid JELEE_TRACE_OTLP_HEADERS")
		}
		c.OTLP.Headers = headers
	}
	return nil
}

func parseHeaderList(value string) (map[string]string, error) {
	headers := map[string]string{}
	for _, pair := range strings.Split(value, ",") {
		if strings.TrimSpace(pair) == "" {
			continue
		}
		name, raw, found := strings.Cut(pair, "=")
		if !found {
			return nil, errors.New("header without value")
		}
		decoded, err := url.PathUnescape(strings.TrimSpace(raw))
		if err != nil {
			return nil, err
		}
		headers[strings.TrimSpace(name)] = decoded
	}
	if len(headers) == 0 {
		return nil, nil
	}
	return headers, nil
}
