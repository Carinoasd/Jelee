package logging

import (
	"context"
	"log/slog"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// traceKey and spanKey are the G46.4 correlation fields. traceId has the same
// value as X-Request-ID and the error envelope traceId for HTTP requests.
const (
	traceKey = "traceId"
	spanKey  = "spanId"
)

// levelHandler applies component thresholds in front of the formatting
// handler and normalizes attribute and group keys, which slog never passes
// through ReplaceAttr for groups. Enabled uses the lowest active threshold as
// a cheap prefilter; Handle then applies the exact threshold of the record's
// component. A nil levels only guards keys.
type levelHandler struct {
	next      slog.Handler
	levels    *levels
	component string
}

func (h *levelHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if h.levels != nil && level < h.levels.min.Level() {
		return false
	}
	return h.next.Enabled(ctx, level)
}

func (h *levelHandler) Handle(ctx context.Context, r slog.Record) error {
	component, unsafe, traced := h.component, false, false
	fields := domain.LogFieldsFrom(ctx)
	r.Attrs(func(a slog.Attr) bool {
		if component == "" && a.Key == "component" && a.Value.Kind() == slog.KindString {
			component = a.Value.String()
		}
		if !unsafe && needsKeyGuard(a) {
			unsafe = true
		}
		if a.Key == traceKey {
			traced = true
		}
		if fields != (domain.LogFields{}) {
			// An explicit attribute wins over the context field.
			clearField(&fields, a.Key)
		}
		return true
	})
	if h.levels != nil && r.Level < h.levels.threshold(component) {
		return nil
	}
	if unsafe {
		clean := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
		r.Attrs(func(a slog.Attr) bool {
			clean.AddAttrs(guardKeys(a))
			return true
		})
		r = clean
	}
	// G46.6: a record logged with a span context carries its identifiers.
	// An explicit traceId attribute wins over the context.
	cloned := unsafe
	if sc, ok := domain.SpanFromContext(ctx); ok && !traced {
		if !cloned {
			r, cloned = r.Clone(), true
		}
		r.AddAttrs(slog.String(traceKey, sc.TraceHex()), slog.String(spanKey, sc.SpanHex()))
	}
	// G46.4: identity fields the middleware or worker attached to the
	// context, in this or a parent goroutine.
	if fields != (domain.LogFields{}) {
		if !cloned {
			r = r.Clone()
		}
		r.AddAttrs(fieldAttrs(fields)...)
	}
	return h.next.Handle(ctx, r)
}

func (h *levelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	component := h.component
	clean := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		if a.Key == "component" && a.Value.Kind() == slog.KindString {
			component = a.Value.String()
		}
		clean = append(clean, guardKeys(a))
	}
	return &levelHandler{next: h.next.WithAttrs(clean), levels: h.levels, component: component}
}

func (h *levelHandler) WithGroup(name string) slog.Handler {
	return &levelHandler{next: h.next.WithGroup(safeKey(name)), levels: h.levels, component: h.component}
}

// safeKey keeps identifier-like keys. Keys are code constants by contract, so
// anything else is treated as data that must not reach the output.
func safeKey(key string) string {
	if key == "" || (len(key) <= 32 && (key[0] < '0' || key[0] > '9') && safeToken(key, 32, "_") && (len(key) < 12 || !strings.ContainsAny(key, "0123456789"))) {
		return key
	}
	return "redactedKey"
}

func needsKeyGuard(a slog.Attr) bool {
	if safeKey(a.Key) != a.Key || a.Value.Kind() == slog.KindLogValuer {
		return true
	}
	if a.Value.Kind() == slog.KindGroup {
		for _, member := range a.Value.Group() {
			if needsKeyGuard(member) {
				return true
			}
		}
	}
	return false
}

func guardKeys(a slog.Attr) slog.Attr {
	a.Key = safeKey(a.Key)
	a.Value = a.Value.Resolve()
	if a.Value.Kind() != slog.KindGroup {
		return a
	}
	members := a.Value.Group()
	clean := make([]slog.Attr, len(members))
	for i, member := range members {
		clean[i] = guardKeys(member)
	}
	return slog.Attr{Key: a.Key, Value: slog.GroupValue(clean...)}
}

// Context field keys (G46.4).
const (
	userIDKey    = "userId"
	deviceIDKey  = "deviceId"
	clientIDKey  = "clientId"
	itemIDKey    = "itemId"
	libraryIDKey = "libraryId"
	taskIDKey    = "taskId"
	jobRunIDKey  = "jobRunId"
)

func clearField(f *domain.LogFields, key string) {
	switch key {
	case userIDKey:
		f.UserID = ""
	case deviceIDKey:
		f.DeviceID = ""
	case clientIDKey:
		f.ClientID = ""
	case itemIDKey:
		f.ItemID = ""
	case libraryIDKey:
		f.LibraryID = ""
	case taskIDKey:
		f.TaskID = ""
	case jobRunIDKey:
		f.JobRunID = ""
	}
}

func fieldAttrs(f domain.LogFields) []slog.Attr {
	attrs := make([]slog.Attr, 0, 7)
	for _, field := range [...]struct{ key, value string }{{userIDKey, f.UserID}, {deviceIDKey, f.DeviceID}, {clientIDKey, f.ClientID},
		{itemIDKey, f.ItemID}, {libraryIDKey, f.LibraryID}, {taskIDKey, f.TaskID}, {jobRunIDKey, f.JobRunID}} {
		if field.value != "" {
			attrs = append(attrs, slog.String(field.key, field.value))
		}
	}
	return attrs
}
