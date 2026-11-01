package logging

import "log/slog"

// SpanAttr passes one span attribute through the record whitelist of
// redact.go before it leaves the process through a trace exporter (G46.5,
// G46.6): a key outside the whitelist, or a value its key does not accept,
// becomes "[redacted]" exactly as in a log record. Developer mode fields
// are always refused.
func SpanAttr(a slog.Attr) slog.Attr {
	return defaultRedactor.replace(nil, a)
}
