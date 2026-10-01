package app

import (
	"context"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// NFOExecutionRepository preserves the B parent fence. Load permits inspection
// of old or invalidated requests; Begin executes only matching trusted identity
// and frozen scope, after inventory completes. Recovery never repins a request.
type NFOExecutionRepository interface {
	NFORepository
	LoadNFOWork(context.Context, domain.JobLease) (domain.NFOWork, error)
	BeginRequestedNFOPhase(context.Context, domain.JobLease) (domain.NFOPhase, error)
	AbortNFORequest(context.Context, domain.JobLease, domain.NFOPhaseError) error
	ClaimJobWithCapabilities(context.Context, string, bool, time.Duration, domain.ScanCapabilities) (domain.JobLease, error)
}
