package app

import (
	"context"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// MetadataProber exposes only the fixed trusted identity and readonly media
// operations. Errors are domain sentinels or standard context errors; every
// failing call returns a zero observation/stamp. No process API crosses this port.
type MetadataProber interface {
	IdentityDigest() string
	Inspect(context.Context, domain.ProbeSource) (domain.ProbeStamp, error)
	Probe(context.Context, domain.ProbeSource) (domain.ProbeObservation, error)
}

// ProbeJobRepository rechecks the live session and admin role in every public
// operation. Identity comes from a trusted local factory, never request JSON.
// Replay compares the retained public intent without repeating generation bumps.
type ProbeJobRepository interface {
	SubmitScanJob(context.Context, domain.Actor, string, string, string, domain.ProbeIntent, domain.JobPolicy, *domain.ProbeIdentity) (domain.Job, bool, error)
	RetryScanJob(context.Context, domain.Actor, string, string, domain.JobPolicy, *domain.ProbeIdentity) (domain.Job, bool, error)
	GetProbeJobSummary(context.Context, domain.Actor, string) (domain.ProbeJobSummary, error)
}

// ProbeExecutionRepository preserves the parent/file lease fencing from the
// cache repository. Worker capability only filters claims; a DB identity never
// grants execution authority. Begin reads the durable request, not caller intent.
type ProbeExecutionRepository interface {
	ProbeRepository
	LoadProbeWork(context.Context, domain.JobLease) (domain.ProbeWork, error)
	BeginRequestedProbePhase(context.Context, domain.JobLease) (domain.ProbePhase, error)
	AbortProbeRequest(context.Context, domain.JobLease, domain.ProbePhaseError) error
	ClaimJobWithProbe(context.Context, string, bool, time.Duration, bool) (domain.JobLease, error)
}
