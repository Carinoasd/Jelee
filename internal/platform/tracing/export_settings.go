package tracing

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/tlstrust"
)

// The OTLP export settings are checked here, without the exporter's
// dependencies, so the configuration loader of every binary can refuse a
// half configured exporter while only the server links the OTLP client.

// Defaults and bounds of the export settings; shared by the configuration
// check and the exporter.
const (
	OTLPDefaultTimeout   = 10 * time.Second
	OTLPDefaultBatchSize = 512
	OTLPDefaultInterval  = 5 * time.Second
	OTLPDefaultQueueSize = 2048

	OTLPMinTimeout   = time.Second
	OTLPMaxTimeout   = time.Minute
	OTLPMaxBatchSize = 4096
	OTLPMinInterval  = 100 * time.Millisecond
	OTLPMaxInterval  = time.Minute
	OTLPMaxQueueSize = 1 << 16
	OTLPMaxHeaders   = 16
	// OTLPMaxHeaderBytes bounds the names and values of all headers together.
	OTLPMaxHeaderBytes = 8192
	// OTLPDefaultPath is the OTLP/HTTP traces path used when the endpoint has
	// none.
	OTLPDefaultPath = "/v1/traces"
)

// OTLPSettings configure the OTLP/HTTP span exporter. Endpoint is an
// absolute http or https URL; http requires Insecure and a loopback IP
// literal. CAFile, only for https, replaces the system roots with the PEM
// certificates it holds. Zero numbers select the defaults.
type OTLPSettings struct {
	Endpoint  string
	Headers   map[string]string
	Insecure  bool
	CAFile    string
	Timeout   time.Duration
	BatchSize int
	Interval  time.Duration
	QueueSize int
}

// Validate checks every setting without echoing the endpoint or headers.
func (s OTLPSettings) Validate() error {
	if err := ValidateOTLPEndpoint(s.Endpoint, s.Insecure); err != nil {
		return err
	}
	if err := ValidateOTLPHeaders(s.Headers); err != nil {
		return err
	}
	if err := ValidateOTLPCAFile(s.CAFile, s.Insecure); err != nil {
		return err
	}
	return ValidateOTLPBounds(s.Timeout, s.BatchSize, s.Interval, s.QueueSize)
}

var errCAInsecure = errors.New("trace export caFile applies to an https endpoint only")

// ValidateOTLPCAFile checks that the CA file, when set, is readable and
// holds only PEM certificates; it is refused together with insecure (plain
// http has no certificate). Errors name neither path nor contents.
func ValidateOTLPCAFile(path string, insecure bool) error {
	if path == "" {
		return nil
	}
	if insecure {
		return errCAInsecure
	}
	if _, err := tlstrust.LoadPool(path); err != nil {
		return fmt.Errorf("trace export caFile: %w", err)
	}
	return nil
}

var (
	errEndpoint = errors.New("trace export endpoint must be an absolute http or https URL without credentials, query or fragment")
	errInsecure = errors.New("trace export over plain http requires insecure and a loopback IP endpoint; insecure is only valid for http")
	errHeaders  = errors.New("trace export headers must be at most 16 valid HTTP header names and values")
	errBounds   = errors.New("trace export timeout, batch, interval or queue setting is outside supported range")
)

// ValidateOTLPEndpoint checks the endpoint and insecure flag without echoing
// either (the URL may name an internal host).
func ValidateOTLPEndpoint(endpoint string, insecure bool) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return errEndpoint
	}
	switch u.Scheme {
	case "https":
		if insecure {
			return errInsecure
		}
	case "http":
		ip := net.ParseIP(strings.Trim(u.Hostname(), "[]"))
		if !insecure || ip == nil || !ip.IsLoopback() {
			return errInsecure
		}
	default:
		return errEndpoint
	}
	return nil
}

// reservedHeaders are set by the exporter itself; overriding them breaks
// the request.
var reservedHeaders = map[string]bool{"content-type": true, "content-length": true, "content-encoding": true, "host": true, "user-agent": true, "connection": true, "transfer-encoding": true}

// ValidateOTLPHeaders checks the extra request headers (authentication tokens
// and tenant identifiers) without echoing them.
func ValidateOTLPHeaders(headers map[string]string) error {
	if len(headers) > OTLPMaxHeaders {
		return errHeaders
	}
	total := 0
	for name, value := range headers {
		total += len(name) + len(value)
		if !headerToken(name) || reservedHeaders[strings.ToLower(name)] || strings.ContainsAny(value, "\x00\r\n") || value == "" {
			return errHeaders
		}
	}
	if total > OTLPMaxHeaderBytes {
		return errHeaders
	}
	return nil
}

func headerToken(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0) {
			return false
		}
	}
	return true
}

// ValidateOTLPBounds checks the queue and batch settings; zero selects the
// default and is accepted.
func ValidateOTLPBounds(timeout time.Duration, batchSize int, interval time.Duration, queueSize int) error {
	if timeout != 0 && (timeout < OTLPMinTimeout || timeout > OTLPMaxTimeout) || batchSize < 0 || batchSize > OTLPMaxBatchSize ||
		interval != 0 && (interval < OTLPMinInterval || interval > OTLPMaxInterval) || queueSize < 0 || queueSize > OTLPMaxQueueSize {
		return errBounds
	}
	return nil
}
