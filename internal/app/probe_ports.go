package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// ProbeRepository is an unwired persistence boundary in stage 3C2A. All work
// operations recheck the live parent job lease and cancellation in PostgreSQL.
// No filesystem access, parsing or process work occurs inside its transactions.
// Lookup/Commit accept at most 16 consecutive next inventory candidates. A
// multi-entry Commit contains only hits; a miss is acquired and committed as
// the first remaining entry, alone. The DB rechecks this prefix and full keys.
// Acquire permits one child lease per parent. ExpiresAt is informational: the
// current DB lease, owner and globally monotonic generation decide validity.
type ProbeRepository interface {
	EnsureProbePolicy(context.Context, domain.ProbeCachePolicy) error
	RegisterProbeIdentity(context.Context, domain.ProbeIdentity) (domain.ProbeIdentityRef, error)
	BeginProbePhase(context.Context, domain.JobLease, domain.ProbePhaseStart) (domain.ProbePhase, error)
	NextProbePage(context.Context, domain.JobLease, int) (domain.ProbePage, error)
	LookupProbeBatch(context.Context, domain.JobLease, domain.ProbePageToken, []domain.ProbeCandidate) ([]domain.ProbeLookup, error)
	AcquireProbe(context.Context, domain.JobLease, domain.ProbePageToken, domain.ProbeCandidate) (domain.ProbeLease, error)
	CommitProbeBatch(context.Context, domain.JobLease, domain.ProbePageToken, []domain.ProbeCompletion) (domain.ProbePhase, error)
	ReleaseProbeLease(context.Context, domain.JobLease, domain.ProbeLease) error
	FinishProbePhase(context.Context, domain.JobLease) (domain.ProbePhase, error)
	AbortProbePhase(context.Context, domain.JobLease, domain.ProbePhaseError) error
	SweepProbeCache(context.Context, int) (domain.ProbeSweepResult, error)
}

// ProbeAdminRepository is not exposed through HTTP in stage 3C2A. Each method
// must recheck the actor's current session and admin role in its transaction.
type ProbeAdminRepository interface {
	InvalidateProbeLibrary(context.Context, domain.Actor, string) error
	InvalidateProbeItem(context.Context, domain.Actor, string) error
}
