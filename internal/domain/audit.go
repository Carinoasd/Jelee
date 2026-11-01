package domain

import (
	"context"
	"encoding/json"
	"time"
)

// Audit categories. Audit rows record administrative and permission changes;
// security rows record authentication failures and similar signals. Each has
// its own retention, independent of ordinary logs.
const (
	AuditCategoryAudit    = "audit"
	AuditCategorySecurity = "security"
)

// AuditRecord is one stored audit row as returned to administrators.
// Exactly one of TargetID (a UUID) or TargetRef (any other target) may be set.
type AuditRecord struct {
	ID         int64           `json:"id"`
	Category   string          `json:"category"`
	Event      string          `json:"event"`
	TargetID   string          `json:"targetId,omitempty"`
	TargetRef  string          `json:"targetRef,omitempty"`
	ActorID    string          `json:"actorId,omitempty"`
	ActorIP    string          `json:"actorIp,omitempty"`
	RequestID  string          `json:"requestId,omitempty"`
	Before     json.RawMessage `json:"before"`
	After      json.RawMessage `json:"after"`
	OccurredAt time.Time       `json:"occurredAt"`
}

// AuditFilter narrows an administrator audit listing. Zero values do not
// filter. Target matches target_id when it is a UUID and target_ref otherwise.
type AuditFilter struct {
	Category string
	Event    string
	ActorID  string
	Target   string
	Since    time.Time
	Until    time.Time
}

// AuditRetention holds the independent retention periods in days.
type AuditRetention struct {
	AuditDays    int       `json:"auditDays"`
	SecurityDays int       `json:"securityDays"`
	Revision     int64     `json:"revision"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// AuditPurgeResult reports how many expired rows one purge batch removed.
type AuditPurgeResult struct {
	Audit    int64 `json:"audit"`
	Security int64 `json:"security"`
	// ArchivedFiles counts the archive files the batch wrote; zero when the
	// archive is off or the files already existed from an earlier attempt.
	ArchivedFiles int `json:"archivedFiles,omitempty"`
}

// AuditArchiveReceipt reports what one Archive call made durable.
type AuditArchiveReceipt struct {
	Files int
	Bytes int64
}

// AuditArchiveSink receives the rows a purge batch is about to delete (G36.3).
// Archive returns nil only once every record is durable (written and synced);
// the purge deletes the rows only after that. Records arrive in ascending ID
// order. An implementation must be idempotent for the same records.
type AuditArchiveSink interface {
	Archive(ctx context.Context, records []AuditRecord) (AuditArchiveReceipt, error)
}

// AuditArchivePrune reports what one archive retention pass removed.
type AuditArchivePrune struct {
	Files int
	Bytes int64
}

const (
	AuditRetentionMinDays = 7
	AuditRetentionMaxDays = 36500
)
