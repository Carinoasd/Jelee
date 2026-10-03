package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func (j *Jobs) catalogSync() (CatalogSyncRepository, error) {
	if j == nil {
		return nil, domain.ErrMetadataUnavailable
	}
	repository, ok := j.repository.(CatalogSyncRepository)
	if !ok {
		return nil, domain.ErrMetadataUnavailable
	}
	return repository, nil
}

// AcceptMissing records an administrator's explicit acceptance of the files
// a reviewed scan found missing. It queues a catalog_sync job that publishes
// that scan as the new baseline under a lease, then synchronises the catalog.
// The expected count must equal the reviewed result; repeating an accepted
// decision is rejected rather than replayed.
func (j *Jobs) AcceptMissing(ctx context.Context, actor domain.Actor, id string, expectedMissing int64) (domain.Job, error) {
	if ctx == nil || !validTarget(actor, id) || expectedMissing < 1 || expectedMissing > 500000 {
		return domain.Job{}, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.Job{}, err
	}
	repository, err := j.catalogSync()
	if err != nil {
		return domain.Job{}, err
	}
	return repository.AcceptInventoryMissing(ctx, actor, id, expectedMissing, j.policy)
}

func (j *Jobs) SubmitCatalogSync(ctx context.Context, actor domain.Actor, library, key, priority string) (domain.Job, bool, error) {
	if ctx == nil || !validTarget(actor, library) || !validKey(key) || priority != domain.JobPriorityManual && priority != domain.JobPriorityBackground {
		return domain.Job{}, false, domain.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return domain.Job{}, false, err
	}
	repository, err := j.catalogSync()
	if err != nil {
		return domain.Job{}, false, err
	}
	return repository.SubmitCatalogSync(ctx, actor, library, key, priority, j.policy)
}

func (j *Jobs) CatalogSyncReport(ctx context.Context, actor domain.Actor, id string) (domain.CatalogSyncReport, error) {
	if ctx == nil || !validTarget(actor, id) {
		return domain.CatalogSyncReport{}, domain.ErrInvalid
	}
	repository, err := j.catalogSync()
	if err != nil {
		return domain.CatalogSyncReport{}, err
	}
	return repository.GetCatalogSyncReport(ctx, actor, id)
}

func (j *Jobs) CatalogSyncSettings(ctx context.Context, actor domain.Actor, library string) (domain.CatalogSyncSettings, error) {
	if ctx == nil || !validTarget(actor, library) {
		return domain.CatalogSyncSettings{}, domain.ErrInvalid
	}
	repository, err := j.catalogSync()
	if err != nil {
		return domain.CatalogSyncSettings{}, err
	}
	return repository.GetCatalogSyncSettings(ctx, actor, library)
}

func (j *Jobs) PutCatalogSyncSettings(ctx context.Context, actor domain.Actor, library string, auto bool) (domain.CatalogSyncSettings, error) {
	if ctx == nil || !validTarget(actor, library) {
		return domain.CatalogSyncSettings{}, domain.ErrInvalid
	}
	repository, err := j.catalogSync()
	if err != nil {
		return domain.CatalogSyncSettings{}, err
	}
	return repository.PutCatalogSyncSettings(ctx, actor, library, auto)
}

func (j *Jobs) CatalogPending(ctx context.Context, actor domain.Actor, library, cursor string, limit int) ([]domain.CatalogPendingEntry, error) {
	if ctx == nil || !validTarget(actor, library) || !validJobPage(actor, cursor, limit) {
		return nil, domain.ErrInvalid
	}
	repository, err := j.catalogSync()
	if err != nil {
		return nil, err
	}
	return repository.ListCatalogPending(ctx, actor, library, cursor, limit)
}
