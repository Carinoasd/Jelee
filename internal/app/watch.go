package app

import (
	"context"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

func NewJobsWithWatch(j *Jobs, repository WatchRepository) (*Jobs, error) {
	if j == nil || j.schedules == nil || repository == nil {
		return nil, domain.ErrInvalid
	}
	copy := *j
	copy.watchRepository = repository
	return &copy, nil
}

func (j *Jobs) WatchStatus(ctx context.Context, actor domain.Actor, library string) (domain.WatchStatus, error) {
	if ctx == nil || !validTarget(actor, library) {
		return domain.WatchStatus{}, domain.ErrInvalid
	}
	if j == nil || j.watchRepository == nil {
		return domain.WatchStatus{}, domain.ErrNotFound
	}
	return j.watchRepository.GetWatchStatus(ctx, actor, library)
}

func (j *Jobs) DispatchWatch(ctx context.Context, lease domain.WatchLease) (bool, error) {
	if ctx == nil || j == nil || j.watchRepository == nil {
		return false, domain.ErrInvalid
	}
	probe, _ := j.currentProbeIdentity()
	nfo, _ := j.currentNFOIdentity()
	return j.watchRepository.DispatchWatch(ctx, lease, j.policy, probe, nfo, j.currentIgnoreCapabilities())
}
