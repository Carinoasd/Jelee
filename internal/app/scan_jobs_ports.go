package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// ScanJobRepository atomically binds public intent, trusted identities and
// generations for both optional stages. Replay rechecks live administration and
// compares retained intent before checking current availability. Nil identities
// reject new requested stages; off may use the compiled default NFO identity,
// which grants no reading authority. Retry copies intent into a new run.
type ScanJobRepository interface {
	SubmitScanWithStages(context.Context, domain.Actor, string, string, string, domain.ScanIntent, domain.JobPolicy, *domain.ProbeIdentity, *domain.NFOIdentity) (domain.Job, bool, error)
	RetryScanWithStages(context.Context, domain.Actor, string, string, domain.JobPolicy, *domain.ProbeIdentity, *domain.NFOIdentity) (domain.Job, bool, error)
}
