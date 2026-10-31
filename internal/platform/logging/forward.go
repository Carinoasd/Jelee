package logging

import (
	"context"
	"io"
)

// ForwarderKind names an external log destination from G46.1.
type ForwarderKind string

const (
	ForwardSyslog ForwarderKind = "syslog"
	ForwardLoki   ForwarderKind = "loki"
	ForwardOTLP   ForwarderKind = "otlp"
)

// Forwarder ships records to an external collector. Each Write receives one
// complete, already redacted JSON record terminated by a newline, delivered
// from a dedicated bounded queue so a slow collector cannot block callers.
// Syslog (forward_syslog.go) and Loki (forward_loki.go) are implemented; OTLP
// is not.
type Forwarder interface {
	io.WriteCloser
	Kind() ForwarderKind
	// Flush pushes buffered records to the collector.
	Flush(ctx context.Context) error
}
