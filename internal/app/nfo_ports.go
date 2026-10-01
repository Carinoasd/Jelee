package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// NFOReader reads and hashes one bounded immutable source before XML parsing.
// Identity is trusted local configuration. Reading retains no open descriptor;
// the observation's bytes describe that read, not a filesystem atomic snapshot.
type NFOReader interface {
	Identity() domain.NFOIdentity
	Read(context.Context, domain.NFOSource) (NFOReadSource, error)
}

// Only fixed XML/encoding failures become an invalid summary with nil error.
// Other failures return a zero summary and a domain or standard context error.
// Parse never reopens the path, and may be called repeatedly or concurrently.
type NFOReadSource interface {
	Stamp() domain.NFOStamp
	Parse(context.Context) (domain.NFOValidationSummary, error)
}

// NFORepository is an unwired DB contract in stage 3C3B. Prepare captures the
// library's NFO generation before any inventory is saved. Begin requires a
// complete inventory and never repins generation or identity. Every operation
// rechecks the current parent owner/generation/lease and cancellation in the DB.
// Filesystem reading, hashing and parsing take place outside transactions.
//
// No child leases are issued: the existing active-job-per-library constraint and
// parent fence serialize writers. A commit contains at most 16 consecutive hits
// or one fresh result, atomically updating quota, cache and checkpoint. Cache
// eviction cannot revive an old parent lease or stale phase revision.
// Stale commit tokens conflict; Load recovers the committed cursor without
// repeating effects. Prepare under off records an aborted/disabled phase with
// immutable Mode=off; this specific phase does not block ordinary inventory.
type NFORepository interface {
	EnsureNFOCachePolicy(context.Context, domain.NFOCachePolicy) error
	PrepareNFOPhase(context.Context, domain.JobLease, domain.NFOIdentity) (domain.NFOPhase, error)
	LoadNFOPhase(context.Context, domain.JobLease) (domain.NFOPhase, error)
	BeginNFOPhase(context.Context, domain.JobLease) (domain.NFOPhase, error)
	NextNFOPage(context.Context, domain.JobLease, int) (domain.NFOPage, error)
	LookupNFOBatch(context.Context, domain.JobLease, domain.NFOPageToken, []domain.NFOCandidate) ([]domain.NFOLookup, error)
	CommitNFOBatch(context.Context, domain.JobLease, domain.NFOPageToken, []domain.NFOCompletion) (domain.NFOPhase, error)
	FinishNFOPhase(context.Context, domain.JobLease) (domain.NFOPhase, error)
	AbortNFOPhase(context.Context, domain.JobLease, domain.NFOPhaseError) error
	SweepNFOCache(context.Context, int) (domain.NFOSweepResult, error)
}

// Public administration rechecks the actor's live session/admin role in the
// transaction, including retained idempotency replays. Mode changes affect only
// the NFO generation. Set compares library/mode/expected generation for actor/key
// replay; a different body conflicts. An identical current mode does not bump.
// Requests have fixed bounded retention; an expired replay still cannot bypass
// expected-generation CAS. No HTTP/CLI wiring is added in stage 3C3B.
type NFOAdminRepository interface {
	GetNFOLibraryPolicy(context.Context, domain.Actor, string) (domain.NFOLibraryPolicy, error)
	SetNFOLibraryPolicy(context.Context, domain.Actor, string, string, int64, string) (domain.NFOLibraryPolicy, bool, error)
}
