package domain

import (
	"errors"
	"time"
)

var (
	ErrJobQueueFull    = errors.New("job queue capacity reached")
	ErrJobBusy         = errors.New("library already has an active job")
	ErrJobLeaseLost    = errors.New("job lease is no longer valid")
	ErrScanUnavailable = errors.New("scan root is unavailable")
	ErrScanLimit       = errors.New("scan resource limit reached")
	ErrScanIO          = errors.New("scan could not read directory")
)

const (
	JobQueued             = "queued"
	JobRunning            = "running"
	JobSucceeded          = "succeeded"
	JobFailed             = "failed"
	JobCancelled          = "cancelled"
	JobPriorityManual     = "manual"
	JobPriorityBackground = "background"
	ScanPathMaxBytes      = 1024
	ScanBatchMaxEntries   = 128
)

// Job is a public summary. Unknown total work and ETA are intentionally absent.
// Inventory is observed metadata, never a request to modify original files.
type Job struct {
	ID              string     `json:"id"`
	LibraryID       string     `json:"libraryId"`
	Kind            string     `json:"kind"`
	State           string     `json:"state"`
	Priority        string     `json:"priority"`
	Attempts        int        `json:"attempts"`
	CancelRequested bool       `json:"cancelRequested"`
	Files           int64      `json:"files"`
	Directories     int64      `json:"directories"`
	Skipped         int64      `json:"skipped"`
	Bytes           int64      `json:"bytes"`
	Missing         int64      `json:"missing"`
	ReviewRequired  bool       `json:"reviewRequired"`
	ErrorCode       string     `json:"errorCode,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
}

type JobPolicy struct {
	QueueLimit          int
	HistoryLimit        int
	MaxEntries          int
	MaxDirectories      int
	MaxAttempts         int
	MissingCountLimit   int
	MissingPercentLimit int
}

// Lease identity is private worker data, never accepted in public JSON.
type JobLease struct {
	Job        Job
	Owner      string
	Generation int64
	ExpiresAt  time.Time
	Policy     JobPolicy
	// RecoveryEpoch is zero for the original job lease. A positive value names a
	// NFO commit recovery lease on the stopped job; Owner is then its holder.
	RecoveryEpoch int64
}

type ScanDirectory struct {
	RootID   string
	RootPath string // Private absolute path from the configured database root.
	Path     string // Slash-separated root-relative path; dot names the root itself.
	// ClaimToken identifies the scan slot holding this directory under the
	// current lease generation. Empty for callers that do not claim.
	ClaimToken string
}

type InventoryEntry struct {
	ID               string `json:"id,omitempty"`
	RootID           string `json:"rootId"`
	Path             string `json:"path"`
	Kind             string `json:"kind"`
	Size             int64  `json:"size"`
	ModifiedUnixNano int64  `json:"modifiedUnixNano"`
}

// Batch entries and subdirectories are immediate children of the current
// directory, expressed relative to its root. Skipped is the directory total
// and is written only with Done=true, so replay cannot double count it.
type ScanBatch struct {
	Entries     []InventoryEntry
	Directories []string
	Skipped     int64
	Done        bool
}

type LibrarySummary struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Roots int    `json:"roots"`
}

type LibraryRegistration struct {
	Library LibrarySummary `json:"library"`
	RootID  string         `json:"rootId"`
}
