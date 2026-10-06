package domain

import (
	"context"
	"sync"
	"time"
)

// Log context fields (G46.4). Middleware and workers attach the identity of
// the request or task to the context; every record logged with that context,
// or with any context derived from it in another goroutine, carries the
// fields without each call site repeating them. Values are identifiers only:
// the log router validates each one again (UUIDs, bounded tokens) and writes
// [redacted] for anything else.

// LogFields are the correlation fields of G46.4 besides traceId, spanId and
// requestId, which the tracing and HTTP layers already supply.
type LogFields struct {
	// UserID is the authenticated user (UUID).
	UserID string
	// DeviceID is the device identifier a native client reported at login.
	DeviceID string
	// ClientID is the server-issued session identifier of the client (UUID).
	ClientID string
	// ItemID and LibraryID name the item or library a handler works on.
	ItemID    string
	LibraryID string
	// TaskID is the job a worker runs; JobRunID names one claim of it
	// (job ID and lease generation).
	TaskID   string
	JobRunID string
}

type logFieldsKey struct{}

// WithLogFields returns ctx carrying f merged over the fields ctx already
// carries: empty members of f keep the inherited value.
func WithLogFields(ctx context.Context, f LogFields) context.Context {
	merged := LogFieldsFrom(ctx)
	for _, pair := range [...]struct {
		dst *string
		src string
	}{{&merged.UserID, f.UserID}, {&merged.DeviceID, f.DeviceID}, {&merged.ClientID, f.ClientID}, {&merged.ItemID, f.ItemID},
		{&merged.LibraryID, f.LibraryID}, {&merged.TaskID, f.TaskID}, {&merged.JobRunID, f.JobRunID}} {
		if pair.src != "" {
			*pair.dst = pair.src
		}
	}
	return context.WithValue(ctx, logFieldsKey{}, merged)
}

// LogFieldsFrom returns the fields attached to ctx, or the zero value.
func LogFieldsFrom(ctx context.Context) LogFields {
	if ctx == nil {
		return LogFields{}
	}
	f, _ := ctx.Value(logFieldsKey{}).(LogFields)
	return f
}

// Security notes (G46.3). Storage and services note security relevant
// outcomes (a failed login, an account lock) that the HTTP layer cannot tell
// apart from other refusals; the request boundary writes them to the
// security log once the request ends.

// SecurityNotes collects the security events noted during one request. It
// is safe for concurrent use.
type SecurityNotes struct {
	mu     sync.Mutex
	events []string
}

type securityNotesKey struct{}

// WithSecurityNotes attaches a fresh collector to ctx.
func WithSecurityNotes(ctx context.Context) (context.Context, *SecurityNotes) {
	notes := &SecurityNotes{}
	return context.WithValue(ctx, securityNotesKey{}, notes), notes
}

// maxSecurityNotes bounds the events one request can record.
const maxSecurityNotes = 8

// NoteSecurityEvent records event (a lower case identifier such as
// "login_failed") for the request of ctx. It is a no-op outside a request.
func NoteSecurityEvent(ctx context.Context, event string) {
	if ctx == nil {
		return
	}
	notes, _ := ctx.Value(securityNotesKey{}).(*SecurityNotes)
	if notes == nil {
		return
	}
	notes.mu.Lock()
	defer notes.mu.Unlock()
	if len(notes.events) < maxSecurityNotes {
		notes.events = append(notes.events, event)
	}
}

// Events returns the noted events in order.
func (n *SecurityNotes) Events() []string {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.events...)
}

// Log settings (G46.2, G46.9) shared by every instance through storage.

// LogLevelOverride is a runtime level of one log scope set by an
// administrator. Component is a scope name, or empty for the global level.
// A zero ExpiresAt never expires.
type LogLevelOverride struct {
	Component string    `json:"component"`
	Level     string    `json:"level"`
	ExpiresAt time.Time `json:"expiresAt,omitzero"`
}

// Active reports whether the override applies at now.
func (o LogLevelOverride) Active(now time.Time) bool {
	return o.ExpiresAt.IsZero() || now.Before(o.ExpiresAt)
}

// LogSettings is the stored logging configuration: runtime level overrides
// and the retention of ordinary log files, with the audit retention that
// is changed through the same administration route.
type LogSettings struct {
	LogDays       int                `json:"logDays"`
	LogMaxTotalMB int                `json:"logMaxTotalMB"`
	Overrides     []LogLevelOverride `json:"overrides"`
	Revision      int64              `json:"revision"`
	UpdatedAt     time.Time          `json:"updatedAt"`
}

// LogRetention is the retention an administrator sets in one request: the
// ordinary log files (G46.9) and the two audit categories.
type LogRetention struct {
	LogDays       int `json:"logDays"`
	LogMaxTotalMB int `json:"logMaxTotalMB"`
	AuditDays     int `json:"auditDays"`
	SecurityDays  int `json:"securityDays"`
}

// Bounds of the stored log retention. Zero switches a limit off.
const (
	LogRetentionMaxDays = 3650
	LogMaxTotalMBMax    = 1 << 20
	// LogOverridesMax bounds the overrides stored at once (one per scope).
	LogOverridesMax = 32
)

// ValidLogRetention reports whether days and totalMB are within bounds.
func ValidLogRetention(days, totalMB int) bool {
	return days >= 0 && days <= LogRetentionMaxDays && totalMB >= 0 && totalMB <= LogMaxTotalMBMax
}
