package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// CatalogSyncRepository rechecks the live administrator session for every
// public operation and confines each call to the job's or library's scope.
type CatalogSyncRepository interface {
	AcceptInventoryMissing(context.Context, domain.Actor, string, int64, domain.JobPolicy) (domain.Job, error)
	SubmitCatalogSync(context.Context, domain.Actor, string, string, string, domain.JobPolicy) (domain.Job, bool, error)
	GetCatalogSyncReport(context.Context, domain.Actor, string) (domain.CatalogSyncReport, error)
	GetCatalogSyncSettings(context.Context, domain.Actor, string) (domain.CatalogSyncSettings, error)
	PutCatalogSyncSettings(context.Context, domain.Actor, string, bool) (domain.CatalogSyncSettings, error)
	ListCatalogPending(context.Context, domain.Actor, string, string, int) ([]domain.CatalogPendingEntry, error)
}

// CatalogSyncExecutionRepository advances one bounded, fenced batch per call.
// It returns true once every phase has committed; FinishCatalogSync then
// records the terminal state under the same lease.
type CatalogSyncExecutionRepository interface {
	AdvanceCatalogSync(context.Context, domain.JobLease) (bool, error)
	FinishCatalogSync(context.Context, domain.JobLease, string, string) error
}
