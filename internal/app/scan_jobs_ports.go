package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// ScanJobRepository atomically binds public intent, trusted identities and
// generations for optional stages. Replay rechecks live administration and
// compares retained intent before checking current availability. Nil identities
// reject new requested stages; off may use the compiled default NFO identity,
// which grants no reading authority. Retry copies intent into a new run.
// Ignore intent is retained with compiled version constants; the C1 repository
// deliberately cannot execute or claim enabled ignore jobs yet. Transports and
// the application service do not expose this unfinished stage.
type ScanJobRepository interface {
	SubmitScanWithStages(context.Context, domain.Actor, string, string, string, domain.ScanIntent, domain.JobPolicy, *domain.ProbeIdentity, *domain.NFOIdentity) (domain.Job, bool, error)
	RetryScanWithStages(context.Context, domain.Actor, string, string, domain.JobPolicy, *domain.ProbeIdentity, *domain.NFOIdentity) (domain.Job, bool, error)
}

// IgnoreAdmissionRepository checks availability only after authorized retained
// replay, and after retry has loaded the parent's immutable ignore intent.
type IgnoreAdmissionRepository interface {
	SubmitScanWithIgnoreCapability(context.Context, domain.Actor, string, string, string, domain.ScanIntent, domain.JobPolicy, *domain.ProbeIdentity, *domain.NFOIdentity, bool) (domain.Job, bool, error)
	RetryScanWithIgnoreCapability(context.Context, domain.Actor, string, string, domain.JobPolicy, *domain.ProbeIdentity, *domain.NFOIdentity, bool) (domain.Job, bool, error)
}

// IgnoreAdmissionCapabilities describes current server readiness. It never
// supplies rule data, filesystem authority, or a caller-selected identity.
type IgnoreAdmissionCapabilities struct {
	Custom bool
	Family bool
}

// FamilyIgnoreAdmissionRepository is explicit opt-in to the composed contract.
// Retained replay precedes readiness checks; retry retains the parent contract.
type FamilyIgnoreAdmissionRepository interface {
	SubmitScanWithIgnoreFamilies(context.Context, domain.Actor, string, string, string, domain.ScanIntent, domain.JobPolicy, *domain.ProbeIdentity, *domain.NFOIdentity, IgnoreAdmissionCapabilities) (domain.Job, bool, error)
	RetryScanWithIgnoreFamilies(context.Context, domain.Actor, string, string, domain.JobPolicy, *domain.ProbeIdentity, *domain.NFOIdentity, IgnoreAdmissionCapabilities) (domain.Job, bool, error)
}
