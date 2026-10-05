package app

import (
	"context"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"time"
)

type DirectoryObserver interface {
	Observe(context.Context, []domain.ScanDirectory, func(context.Context) error) error
}

type WatchRepository interface {
	GetWatchStatus(context.Context, domain.Actor, string) (domain.WatchStatus, error)
	ClaimWatch(context.Context, string, time.Duration) (domain.WatchLease, error)
	RenewWatch(context.Context, domain.WatchLease, time.Duration) (bool, error)
	MarkWatchDirty(context.Context, domain.WatchLease) error
	ReleaseWatch(context.Context, domain.WatchLease, string) error
	DispatchWatch(context.Context, domain.WatchLease, domain.JobPolicy, *domain.ProbeIdentity, *domain.NFOIdentity, IgnoreAdmissionCapabilities) (bool, error)
}
