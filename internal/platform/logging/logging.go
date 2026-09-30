package logging

import (
	"io"
	"log/slog"
	"strings"
)

// New deliberately permits only bounded operational fields. Request text,
// credentials, paths and arbitrary error strings are never safe log attributes.
func New(output io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
		switch a.Key {
		case slog.TimeKey, slog.LevelKey, slog.MessageKey, "component", "requestId", "method", "status", "durationMs", "event", "count":
			return a
		default:
			return slog.String(a.Key, "[redacted]")
		}
	}}))
}

func SafeMethod(method string) string {
	switch strings.ToUpper(method) {
	case "GET", "POST", "HEAD", "PUT", "PATCH", "DELETE", "OPTIONS":
		return strings.ToUpper(method)
	}
	return "OTHER"
}
