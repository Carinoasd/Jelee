package logging

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MoYuanCN/Jelee/internal/platform/tlstrust"
)

// OTLP/HTTP log forwarding (G46.1). The checks and the HTTP client live here,
// without the OpenTelemetry modules, so the configuration loader of every
// binary can refuse a half configured destination while only the server
// links the OTLP encoder: the sub-package otlplog registers the sender with
// RegisterOTLPSender when it is linked.

// OTLP bounds shared by the configuration check and the sender.
const (
	// OTLPLogsPath is appended to an endpoint that does not already end in
	// it.
	OTLPLogsPath     = "/v1/logs"
	OTLPMaxHeaders   = 16
	OTLPMaxHeaderLen = 8192
)

// OTLPOptions selects the OTLP/HTTP logs destination.
type OTLPOptions struct {
	// Endpoint is the collector's base URL (http or https); /v1/logs is
	// appended unless the path already ends in it.
	Endpoint string
	// Headers are sent with every push (authentication, tenant). They are
	// secrets: never logged or echoed in errors.
	Headers map[string]string
	// Insecure allows plain http, only to a loopback IP literal.
	Insecure bool
	// CAFile is a PEM file of one or more CA certificates; set, only they
	// are trusted for the https endpoint instead of the system roots.
	CAFile string
}

func (o OTLPOptions) set() bool {
	return o.Endpoint != "" || len(o.Headers) > 0 || o.Insecure || o.CAFile != ""
}

var (
	errOTLPEndpoint = errors.New("log forwarder otlp endpoint must be an absolute http or https URL without credentials, query or fragment")
	errOTLPInsecure = errors.New("log forwarder otlp over plain http requires insecure and a loopback IP endpoint; insecure is only valid for http")
	errOTLPHeaders  = errors.New("log forwarder otlp headers must be at most 16 valid HTTP header names and values")
)

// otlpReservedHeaders are set by the sender itself.
var otlpReservedHeaders = map[string]bool{"content-type": true, "content-length": true, "content-encoding": true, "host": true, "user-agent": true, "connection": true, "transfer-encoding": true}

func (o OTLPOptions) validate() error {
	if o.Endpoint == "" {
		return errors.New("log forwarder otlp requires an endpoint")
	}
	u, err := o.parseURL()
	if err != nil {
		return err
	}
	if len(o.Headers) > OTLPMaxHeaders {
		return errOTLPHeaders
	}
	total := 0
	for name, value := range o.Headers {
		total += len(name) + len(value)
		if !safeToken(name, 128, "!#$%&'*+-.^_`|~") || otlpReservedHeaders[strings.ToLower(name)] || value == "" || strings.ContainsAny(value, "\x00\r\n") {
			return errOTLPHeaders
		}
	}
	if total > OTLPMaxHeaderLen {
		return errOTLPHeaders
	}
	if o.CAFile != "" {
		if u.Scheme != "https" {
			return errors.New("log forwarder otlp caFile requires an https endpoint")
		}
		if _, err := tlstrust.LoadPool(o.CAFile); err != nil {
			return fmt.Errorf("log forwarder otlp caFile: %w", err)
		}
	}
	return nil
}

// parseURL validates the endpoint and the insecure flag and returns the logs
// URL. Plain http needs insecure and a loopback IP literal (a name could
// resolve elsewhere); https refuses insecure.
func (o OTLPOptions) parseURL() (*url.URL, error) {
	u, err := url.Parse(o.Endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return nil, errOTLPEndpoint
	}
	if port := u.Port(); port != "" {
		if n, err := parsePort(port); err != nil || n == 0 {
			return nil, errOTLPEndpoint
		}
	}
	switch u.Scheme {
	case "https":
		if o.Insecure {
			return nil, errOTLPInsecure
		}
	case "http":
		ip := net.ParseIP(u.Hostname())
		if !o.Insecure || ip == nil || !ip.IsLoopback() {
			return nil, errOTLPInsecure
		}
	default:
		return nil, errOTLPEndpoint
	}
	path := strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(path, OTLPLogsPath) {
		path += OTLPLogsPath
	}
	u.Path, u.RawPath = path, ""
	return u, nil
}

// OTLPTarget is what the registered sender receives: a validated logs URL,
// a copy of the headers and the only HTTP client it may use.
type OTLPTarget struct {
	// URL is the full logs URL, ending in /v1/logs.
	URL     string
	Headers map[string]string
	// Client sends to the configured endpoint only: no proxy from the
	// environment, no redirects, TLS 1.2 or later with the system roots or
	// only the CA file's certificates.
	Client  *http.Client
	Timeout time.Duration
}

// ForwardSender pushes one batch of complete, redacted JSON records (without
// the trailing newline) to a collector. It is called from one goroutine
// only. A failure should be built with ForwardFailure or
// ForwardNetworkFailure; any other error counts as a retryable network
// failure.
type ForwardSender interface {
	Send(ctx context.Context, records [][]byte) error
	Close()
}

// OTLPSenderFactory builds the sender of an otlp forwarder.
type OTLPSenderFactory func(OTLPTarget) (ForwardSender, error)

var otlpFactory atomic.Pointer[OTLPSenderFactory]

// RegisterOTLPSender installs the sender of the otlp kind. The otlplog
// package calls it when it is linked; a binary without it refuses an otlp
// forwarder when the router opens.
func RegisterOTLPSender(f OTLPSenderFactory) {
	if f == nil {
		otlpFactory.Store(nil)
		return
	}
	otlpFactory.Store(&f)
}

// ForwardFailure describes a failed push. class is a fixed word safe to print
// (never an address, credential or response body); retry is false for a
// rejection a retry cannot cure; wait is a collector requested minimum delay.
func ForwardFailure(class string, retry bool, wait time.Duration) error {
	return &deliverError{class: class, retry: retry, wait: wait}
}

// ForwardNetworkFailure classifies a transport error as network, timeout or
// tls; the error text, which may hold addresses, is not kept.
func ForwardNetworkFailure(err error) error { return classifyNetError(err) }

// ForwardRetryAfter parses a Retry-After header in seconds, capped at the
// longest retry wait.
func ForwardRetryAfter(v string) time.Duration { return retryAfter(v) }

// externalSender adapts a ForwardSender to the forwarder core.
type externalSender struct{ s ForwardSender }

func (e externalSender) send(ctx context.Context, records [][]byte) error {
	err := e.s.Send(ctx, records)
	if err == nil {
		return nil
	}
	var de *deliverError
	if errors.As(err, &de) {
		return de
	}
	return classifyNetError(err)
}

func (e externalSender) close() { e.s.Close() }

var errOTLPUnavailable = errors.New("log forwarder kind otlp is not available in this program")

func newOTLPSender(o OTLPOptions, timeout time.Duration, pool *x509.CertPool) (batchSender, error) {
	factory := otlpFactory.Load()
	if factory == nil {
		return nil, errOTLPUnavailable
	}
	u, err := o.parseURL()
	if err != nil {
		return nil, err
	}
	headers := make(map[string]string, len(o.Headers))
	for name, value := range o.Headers {
		headers[name] = value
	}
	client := newOTLPClient(timeout, pool)
	s, err := (*factory)(OTLPTarget{URL: u.String(), Headers: headers, Client: client, Timeout: timeout})
	if err != nil {
		client.CloseIdleConnections()
		// The factory's error may quote the endpoint.
		return nil, errors.New("log forwarder otlp sender could not be created")
	}
	return externalSender{s}, nil
}

// newOTLPClient sends to the configured endpoint only: no proxy from the
// environment, no redirects (a collector that redirects would receive the
// headers' tokens at an address nobody configured).
func newOTLPClient(timeout time.Duration, pool *x509.CertPool) *http.Client {
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:       tlstrust.ClientConfig(pool),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          2,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
	}
	return &http.Client{Transport: transport, Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
