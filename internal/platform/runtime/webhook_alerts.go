package runtime

import (
	"context"
	"log/slog"

	"github.com/MoYuanCN/Jelee/internal/app"
)

// alertHandler raises system.alert (G12.1) for warnings and errors logged
// with a code app.SystemAlertCode accepts, then logs the record as before.
// Only the fixed code and component attributes reach the alert; the message
// and every other attribute stay in the log. It sees records at Warn and
// above even when the log level hides them, so an alert does not depend on
// the configured verbosity.
type alertHandler struct {
	inner  slog.Handler
	alerts *app.SystemAlerts
	// code and component come from top-level With attributes; grouped
	// attributes never name the alert.
	code, component string
	grouped         bool
}

func newAlertHandler(inner slog.Handler, alerts *app.SystemAlerts) slog.Handler {
	if alerts == nil {
		return inner
	}
	return &alertHandler{inner: inner, alerts: alerts}
}

func (h *alertHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= slog.LevelWarn || h.inner.Enabled(ctx, level)
}

func (h *alertHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelWarn {
		code, component := h.code, h.component
		if !h.grouped {
			r.Attrs(func(a slog.Attr) bool {
				switch a.Key {
				case "code":
					code = a.Value.String()
				case "component":
					component = a.Value.String()
				}
				return true
			})
		}
		if code != "" {
			severity := "warning"
			if r.Level >= slog.LevelError {
				severity = "error"
			}
			h.alerts.Raise(app.SystemAlert{Code: code, Component: component, Severity: severity})
		}
	}
	if !h.inner.Enabled(ctx, r.Level) {
		return nil
	}
	return h.inner.Handle(ctx, r)
}

func (h *alertHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := *h
	next.inner = h.inner.WithAttrs(attrs)
	if !h.grouped {
		for _, a := range attrs {
			switch a.Key {
			case "code":
				next.code = a.Value.String()
			case "component":
				next.component = a.Value.String()
			}
		}
	}
	return &next
}

func (h *alertHandler) WithGroup(name string) slog.Handler {
	next := *h
	next.inner = h.inner.WithGroup(name)
	if name != "" {
		next.grouped = true
	}
	return &next
}
