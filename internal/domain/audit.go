package domain

import (
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
}

const (
	AuditRetentionMinDays = 7
	AuditRetentionMaxDays = 36500
)
