package app

import (
	"context"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"time"
)

// JobRepository rechecks the live session and administrator role for every
// public operation. Retention bounds the lifetime of idempotency replay.
type JobRepository interface {
	SubmitJob(context.Context, domain.Actor, string, string, string, domain.JobPolicy) (domain.Job, bool, error)
	RetryJob(context.Context, domain.Actor, string, string, domain.JobPolicy) (domain.Job, bool, error)
	CancelJob(context.Context, domain.Actor, string) (domain.Job, error)
	GetJob(context.Context, domain.Actor, string) (domain.Job, error)
	ListJobs(context.Context, domain.Actor, string, int, string) ([]domain.Job, error)
	ListInventory(context.Context, domain.Actor, string, string, int) ([]domain.InventoryEntry, error)
	ListLibraries(context.Context, domain.Actor, string, int) ([]domain.LibrarySummary, error)
}

// JobExecutionRepository uses fenced leases. Stale owners cannot checkpoint,
// complete, renew or release a job. Methods use short transactions only.
type JobExecutionRepository interface {
	ClaimJob(context.Context, string, bool, time.Duration) (domain.JobLease, error)
	HeartbeatJob(context.Context, domain.JobLease, time.Duration) (bool, error)
	NextScanDirectory(context.Context, domain.JobLease) (domain.ScanDirectory, error)
	SaveScanBatch(context.Context, domain.JobLease, domain.ScanDirectory, domain.ScanBatch) error
	FinishJob(context.Context, domain.JobLease, string, string) error
	ReleaseJob(context.Context, domain.JobLease) error
}

// InventoryScanner streams bounded batches from one directory. It never
// modifies media or derives catalog entries, and never follows external roots.
type InventoryScanner interface {
	ScanDirectory(context.Context, domain.ScanDirectory, func(domain.ScanBatch) error) error
}

// JobCancellationNotifier promptly signals local work after the repository has
// committed an authorized cancellation. Durable flags and lease fences remain
// authoritative when the owner is elsewhere or no local execution exists.
type JobCancellationNotifier interface {
	NotifyJobCancellation(string)
}

// JobCancellationReader provides a bounded, read-only check of the committed
// flag for the exact live owner/generation. It never renews a lease.
type JobCancellationReader interface {
	ReadJobCancellation(context.Context, domain.JobLease) (bool, error)
}

// JobPauseRepository returns a live lease to the queue for a planned window
// closure. Completed checkpoints remain; this claim does not consume a failure
// attempt. Persisted cancellation wins. Stale or expired leases cannot pause.
type JobPauseRepository interface {
	PauseJob(context.Context, domain.JobLease) error
}
