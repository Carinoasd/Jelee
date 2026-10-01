package logging

import (
	"github.com/MoYuanCN/Jelee/internal/domain"
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
		case "taskId":
			if a.Value.Kind() == slog.KindString && domain.ValidID(a.Value.String()) {
				return a
			}
		case "state":
			if a.Value.Kind() == slog.KindString {
				switch a.Value.String() {
				case domain.JobQueued, domain.JobRunning, domain.JobSucceeded, domain.JobFailed, domain.JobCancelled:
					return a
				}
			}
		case "code":
			if a.Value.Kind() == slog.KindString {
				switch a.Value.String() {
				case "", "scan_unavailable", "scan_io", "scan_limit", "scan_failed", "job_timeout", "job_attempts_exhausted", "job_lease_lost":
					return a
				}
			}
		default:
			return slog.String(a.Key, "[redacted]")
		}
		return slog.String(a.Key, "[redacted]")
	}}))
}

func SafeMethod(method string) string {
	switch strings.ToUpper(method) {
	case "GET", "POST", "HEAD", "PUT", "PATCH", "DELETE", "OPTIONS":
		return strings.ToUpper(method)
	}
	return "OTHER"
}
