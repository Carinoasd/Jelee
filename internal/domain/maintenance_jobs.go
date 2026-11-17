package domain

import (
	"errors"
	"time"
)

// Maintenance jobs (G13.6, G14.7).
//
// subtitle_cache_cleanup and backup_reminder work on the whole instance: the
// extraction cache and the metadata backups are not per library. A job row
// always names a library and a library has at most one active job, so these
// two are anchored to an idle library at admission; the anchor only decides
// which library's queue slot the job occupies. external_metadata_removal works
// on one library.

// Job kinds.
const (
	JobSubtitleCacheCleanup    = "subtitle_cache_cleanup"
	JobBackupReminder          = "backup_reminder"
	JobExternalMetadataRemoval = "external_metadata_removal"
)

// ErrSubtitleCacheUnavailable refuses a cache cleanup on a server without a
// configured, private extraction cache.
var ErrSubtitleCacheUnavailable = errors.New("subtitle extraction cache is unavailable")

// SubtitleCacheBatchSize bounds the source directories one checkpoint covers.
const SubtitleCacheBatchSize = 64

// Idle limits of a cache cleanup, in days.
const (
	SubtitleCacheIdleDaysDefault = 30
	SubtitleCacheIdleDaysMax     = 3650
)

// Subtitle cache cleanup phases.
const (
	SubtitleCachePhaseSources  = "sources"
	SubtitleCachePhaseCapacity = "capacity"
	SubtitleCachePhaseDone     = "done"
)

// SubtitleCacheSweep is what one cleanup step removed. Orphaned counts whole
// source directories of sources that no longer exist; Revisions counts
// removed revisions (idle, unreadable or crashed staging) of live sources;
// Capacity counts revisions removed to fit the size bound.
type SubtitleCacheSweep struct {
	Orphaned  int
	Revisions int
	Capacity  int
	Failed    int
	Bytes     int64
}

// Add accumulates other into s.
func (s *SubtitleCacheSweep) Add(other SubtitleCacheSweep) {
	s.Orphaned += other.Orphaned
	s.Revisions += other.Revisions
	s.Capacity += other.Capacity
	s.Failed += other.Failed
	s.Bytes += other.Bytes
}

// ValidSubtitleCacheSweep reports non-negative counts.
func ValidSubtitleCacheSweep(s SubtitleCacheSweep) bool {
	return s.Orphaned >= 0 && s.Revisions >= 0 && s.Capacity >= 0 && s.Failed >= 0 && s.Bytes >= 0
}

// SubtitleCacheCleanupJobState is the public progress of a cleanup.
// CursorSource is the last source directory finished, null before the first
// checkpoint. Sources counts directories finished without failure; Failed
// directories or revisions could not be removed and stay for a later run.
type SubtitleCacheCleanupJobState struct {
	JobID        string    `json:"jobId"`
	MaxIdleDays  int       `json:"maxIdleDays"`
	Phase        string    `json:"phase"`
	CursorSource *string   `json:"cursorSource"`
	Sources      int       `json:"sources"`
	Orphaned     int       `json:"orphaned"`
	Revisions    int       `json:"revisions"`
	Capacity     int       `json:"capacity"`
	Failed       int       `json:"failed"`
	FreedBytes   int64     `json:"freedBytes"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// SubtitleCacheCleanupBatch is the next step of a cleanup: the stored state
// and, for the sources phase, which of the listed source IDs still exist.
type SubtitleCacheCleanupBatch struct {
	State SubtitleCacheCleanupJobState
	Live  map[string]bool
}

// Backup reminder thresholds, in days, and statuses.
const (
	BackupReminderDaysDefault = 7
	BackupReminderDaysMax     = 365
	BackupReminderOK          = "ok"
	BackupReminderOverdue     = "overdue"
	BackupReminderNever       = "never"
	BackupReminderManual      = "manual"
	BackupReminderSchedule    = "schedule"
	// BackupReminderInterval spaces scheduled reminders.
	BackupReminderInterval = 24 * time.Hour
)

// BackupStatus is the result of one backup check. LastExportAt is the last
// recorded metadata export, nil when none is recorded (never made, or older
// than the audit retention). AgeSeconds is nil with it.
type BackupStatus struct {
	Status        string     `json:"status"`
	ThresholdDays int        `json:"thresholdDays"`
	LastExportAt  *time.Time `json:"lastExportAt"`
	AgeSeconds    *int64     `json:"ageSeconds"`
	CheckedAt     time.Time  `json:"checkedAt"`
}

// EvaluateBackupStatus compares the last metadata export with the threshold.
// An export exactly threshold days old is still on time; a future time (clock
// skew between hosts) counts as age zero.
func EvaluateBackupStatus(now time.Time, last *time.Time, thresholdDays int) BackupStatus {
	v := BackupStatus{Status: BackupReminderNever, ThresholdDays: thresholdDays, CheckedAt: now.UTC()}
	if last == nil {
		return v
	}
	at := last.UTC()
	age := now.Sub(at)
	if age < 0 {
		age = 0
	}
	seconds := int64(age / time.Second)
	v.LastExportAt, v.AgeSeconds = &at, &seconds
	v.Status = BackupReminderOK
	if age > time.Duration(thresholdDays)*24*time.Hour {
		v.Status = BackupReminderOverdue
	}
	return v
}

// ValidBackupReminderDays reports a threshold inside the supported range.
func ValidBackupReminderDays(days int) bool {
	return days >= 1 && days <= BackupReminderDaysMax
}

// BackupReminderJobState is the public result of a reminder job; Result is
// nil until the check ran.
type BackupReminderJobState struct {
	JobID         string        `json:"jobId"`
	Trigger       string        `json:"trigger"`
	ThresholdDays int           `json:"thresholdDays"`
	Result        *BackupStatus `json:"result"`
	UpdatedAt     time.Time     `json:"updatedAt"`
}

// ExternalMetadataRemovalBatchSize bounds the items one read covers; every
// item is checkpointed on its own.
const ExternalMetadataRemovalBatchSize = 32

// ExternalMetadataRemovalMaxConsecutiveFailures ends a run after this many
// items in a row failed.
const ExternalMetadataRemovalMaxConsecutiveFailures = 10

// ExternalMetadataRemovalOutcome classifies one item of a removal job.
// Removed items lost at least one provider field; Kept items have provider
// fields left only because every one is locked or required; Skipped items
// had no provider field any more or were deleted meanwhile; Failed items
// could not be changed and were left as they were.
type ExternalMetadataRemovalOutcome struct {
	Removed bool
	Kept    bool
	Skipped bool
	Fields  int
	Reviews int
}

// ExternalMetadataRemovalJobState is the public progress of a removal.
type ExternalMetadataRemovalJobState struct {
	JobID               string    `json:"jobId"`
	LibraryID           string    `json:"libraryId"`
	DropRejectedReviews bool      `json:"dropRejectedReviews"`
	CursorItemID        *string   `json:"cursorItemId"`
	Removed             int       `json:"removed"`
	Fields              int       `json:"fields"`
	Kept                int       `json:"kept"`
	Skipped             int       `json:"skipped"`
	Failed              int       `json:"failed"`
	Reviews             int       `json:"reviews"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

// ExternalMetadataRemovalInput is a removal request. Confirm must be true;
// DropRejectedReviews also deletes the administrator's rejections of
// automatic matches, which lets later automatic refreshes match the items
// again.
type ExternalMetadataRemovalInput struct {
	Priority            string
	Confirm             bool
	DropRejectedReviews bool
}
