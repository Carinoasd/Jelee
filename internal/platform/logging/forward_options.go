package logging

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"runtime"
	"strings"
	"time"
)

// ForwarderOptions configures one log forwarder (G46.1). Its errors never
// contain configured values, which may hold credentials.
type ForwarderOptions struct {
	Kind ForwarderKind
	// Name is the destination label of the forwarder metrics; empty selects
	// the kind (NewForwarders makes duplicates unique).
	Name string
	// QueueEntries bounds the forwarder's own queue (default 4096).
	QueueEntries int
	// BatchSize is the most records per push (default 100).
	BatchSize int
	// BatchInterval is the longest a record waits for a batch to fill
	// (default 1s).
	BatchInterval time.Duration
	// Timeout bounds one connection attempt or push (default 10s).
	Timeout time.Duration
	Syslog  SyslogOptions
	Loki    LokiOptions
}

// SyslogOptions selects the RFC 5424 destination.
type SyslogOptions struct {
	// Network is udp (default), tcp, tls, unix (stream) or unixgram.
	Network string
	// Address is host:port, or a socket path for unix and unixgram.
	Address string
	// Facility is a name such as user, daemon, auth, authpriv or local0 to
	// local7 (default local0).
	Facility string
	// AppName is the APP-NAME field (default jelee).
	AppName string
	// Hostname is the HOSTNAME field; empty uses the machine host name.
	Hostname string
	// Framing is octet-counting (default) or newline; it applies to the
	// stream networks only.
	Framing string
}

// LokiOptions selects the Loki push destination.
type LokiOptions struct {
	// URL is the Loki base URL, or the full push URL.
	URL string
	// Username and Password select basic authentication; BearerToken selects
	// bearer authentication. They are exclusive.
	Username    string
	Password    string
	BearerToken string
	// TenantID is sent as X-Scope-OrgID for multi-tenant Loki.
	TenantID string
	// Service is the value of the service label (default jelee).
	Service string
}

const lokiPushPath = "/loki/api/v1/push"

// ValidateForwarder checks one forwarder configuration. goos is the platform
// the forwarder will run on, so the Windows rule is testable everywhere.
func (o ForwarderOptions) validate(goos string) error {
	if o.Name != "" && !validDestinationName(o.Name) {
		return errors.New("log forwarder name must be 1-32 lowercase letters, digits, - or _")
	}
	if o.QueueEntries < 0 || o.QueueEntries > MaxForwardQueueEntries {
		return errors.New("log forwarder queue size is outside supported range")
	}
	if o.BatchSize < 0 || o.BatchSize > MaxForwardBatchSize {
		return errors.New("log forwarder batch size is outside supported range")
	}
	if o.BatchInterval < 0 || (o.BatchInterval != 0 && (o.BatchInterval < MinForwardBatchInterval || o.BatchInterval > MaxForwardBatchInterval)) {
		return errors.New("log forwarder batch interval is outside supported range")
	}
	if o.Timeout < 0 || o.Timeout > MaxForwardTimeout {
		return errors.New("log forwarder timeout is outside supported range")
	}
	switch o.Kind {
	case ForwardSyslog:
		if o.Loki != (LokiOptions{}) {
			return errors.New("log forwarder of kind syslog has Loki options")
		}
		return o.Syslog.validate(goos)
	case ForwardLoki:
		if o.Syslog != (SyslogOptions{}) {
			return errors.New("log forwarder Loki log forwarder has syslog options")
		}
		return o.Loki.validate()
	case ForwardOTLP:
		return errors.New("log forwarder kind otlp is not available")
	default:
		return errors.New("unknown log forwarder kind")
	}
}

// Validate checks the configuration for the running platform.
func (o ForwarderOptions) Validate() error { return o.validate(runtime.GOOS) }

func validDestinationName(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	for _, ch := range name {
		if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
			return false
		}
	}
	return true
}

func (o SyslogOptions) network() string {
	if o.Network == "" {
		return "udp"
	}
	return o.Network
}

func (o SyslogOptions) validate(goos string) error {
	network := o.network()
	switch network {
	case "udp", "tcp", "tls":
		host, port, err := net.SplitHostPort(o.Address)
		if err != nil || host == "" || strings.ContainsAny(o.Address, "\x00\r\n \t/") {
			return errors.New("syslog address must be host:port")
		}
		if n, err := parsePort(port); err != nil || n == 0 {
			return errors.New("syslog address must be host:port")
		}
	case "unix", "unixgram":
		if goos == "windows" {
			return errors.New("syslog unix sockets are not supported on Windows")
		}
		if o.Address == "" || len(o.Address) > 107 || strings.ContainsAny(o.Address, "\x00\r\n") || o.Address[0] != '/' {
			return errors.New("syslog socket address must be an absolute path")
		}
	default:
		return errors.New("syslog network must be udp, tcp, tls, unix or unixgram")
	}
	if _, ok := syslogFacility(o.Facility); !ok {
		return errors.New("unknown syslog facility")
	}
	switch o.Framing {
	case "", "octet-counting", "newline":
	default:
		return errors.New("syslog framing must be octet-counting or newline")
	}
	if o.AppName != "" && !printableASCII(o.AppName, 48) {
		return errors.New("syslog app name must be up to 48 printable ASCII characters")
	}
	if o.Hostname != "" && !printableASCII(o.Hostname, 255) {
		return errors.New("syslog hostname must be up to 255 printable ASCII characters")
	}
	return nil
}

// printableASCII reports whether s is 1..max characters of RFC 5424
// PRINTUSASCII (33-126).
func printableASCII(s string, limit int) bool {
	if s == "" || len(s) > limit {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 33 || s[i] > 126 {
			return false
		}
	}
	return true
}

func (o LokiOptions) validate() error {
	u, err := o.parseURL()
	if err != nil {
		return err
	}
	if (o.Username == "") != (o.Password == "") {
		return errors.New("log forwarder Loki basic authentication needs both username and password")
	}
	if o.BearerToken != "" && o.Username != "" {
		return errors.New("log forwarder Loki accepts either basic or bearer authentication")
	}
	if strings.ContainsAny(o.Username, ":\x00\r\n") || strings.ContainsAny(o.Password, "\x00\r\n") {
		return errors.New("invalid Loki credentials")
	}
	if o.BearerToken != "" && !printableASCII(o.BearerToken, 4096) {
		return errors.New("invalid Loki bearer token")
	}
	if (o.Username != "" || o.BearerToken != "") && u.Scheme != "https" && !loopbackHost(u.Hostname()) {
		return errors.New("log forwarder Loki credentials require https unless the host is loopback")
	}
	if o.TenantID != "" && !validTenant(o.TenantID) {
		return errors.New("invalid Loki tenant id")
	}
	if o.Service != "" && !safeToken(o.Service, 64, "_.-") {
		return errors.New("invalid Loki service label")
	}
	return nil
}

func validTenant(s string) bool {
	if len(s) > 150 {
		return false
	}
	for _, ch := range s {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '.' || ch == '-') {
			return false
		}
	}
	return true
}

// parseURL validates the base URL and returns the push endpoint.
func (o LokiOptions) parseURL() (*url.URL, error) {
	u, err := url.Parse(o.URL)
	if err != nil || o.URL == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, errors.New("log forwarder Loki URL must be an http or https URL")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return nil, errors.New("log forwarder Loki URL must not carry credentials, a query or a fragment")
	}
	if port := u.Port(); port != "" {
		if n, err := parsePort(port); err != nil || n == 0 {
			return nil, errors.New("invalid Loki URL port")
		}
	}
	path := strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(path, lokiPushPath) {
		path += lokiPushPath
	}
	u.Path, u.RawPath = path, ""
	return u, nil
}

func parsePort(s string) (int, error) {
	n := 0
	if s == "" || len(s) > 5 {
		return 0, fmt.Errorf("invalid port")
	}
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("invalid port")
		}
		n = n*10 + int(ch-'0')
	}
	if n > 65535 {
		return 0, fmt.Errorf("invalid port")
	}
	return n, nil
}

// NewForwarders builds the configured forwarders, giving duplicate names a
// numeric suffix. On error nothing is left running.
func NewForwarders(opts []ForwarderOptions) ([]Forwarder, error) {
	seen := map[string]int{}
	out := make([]Forwarder, 0, len(opts))
	for _, o := range opts {
		if o.Name == "" {
			o.Name = string(o.Kind)
		}
		seen[o.Name]++
		if n := seen[o.Name]; n > 1 {
			o.Name = fmt.Sprintf("%s-%d", o.Name, n)
			if !validDestinationName(o.Name) {
				o.Name = fmt.Sprintf("fwd-%d", len(out)+1)
			}
		}
		f, err := NewForwarder(o)
		if err != nil {
			for _, built := range out {
				_ = built.Close()
			}
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// NewForwarder builds and starts one forwarder.
func NewForwarder(o ForwarderOptions) (Forwarder, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	if o.Name == "" {
		o.Name = string(o.Kind)
	}
	timeout := o.Timeout
	if timeout == 0 {
		timeout = DefaultForwardTimeout
	}
	co := forwardCoreOptions{name: o.Name, kind: o.Kind, queue: o.QueueEntries, batchSize: o.BatchSize, batchInterval: o.BatchInterval}
	switch o.Kind {
	case ForwardSyslog:
		s := newSyslogSender(o.Syslog, timeout)
		co.maxRecord = s.maxRecord()
		return newForwardCore(co, s), nil
	default:
		s, err := newLokiSender(o.Loki, timeout)
		if err != nil {
			return nil, err
		}
		return newForwardCore(co, s), nil
	}
}

// loopbackHost reports whether host names the local machine.
func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
