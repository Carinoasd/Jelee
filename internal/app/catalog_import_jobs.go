package app

import (
	"context"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

func (j *Jobs) SubmitCatalogImport(ctx context.Context, actor domain.Actor, source, key, priority string, items []domain.CatalogImportSelection) (domain.Job, bool, error) {
	if ctx == nil || !validTarget(actor, source) || !validKey(key) || !domain.ValidCatalogImportSelections(items) {
		return domain.Job{}, false, domain.ErrInvalid
	}
	if j == nil || j.importVerifier == nil {
		return domain.Job{}, false, domain.ErrMetadataUnavailable
	}
	repository, ok := j.importRepository.(CatalogImportRepository)
	if !ok {
		return domain.Job{}, false, domain.ErrMetadataUnavailable
	}
	return repository.SubmitCatalogImport(ctx, actor, source, key, priority, items, j.policy)
}

func (j *Jobs) CatalogImportReport(ctx context.Context, actor domain.Actor, id string) (domain.CatalogImportReport, error) {
	if ctx == nil || !validTarget(actor, id) {
		return domain.CatalogImportReport{}, domain.ErrInvalid
	}
	if j == nil {
		return domain.CatalogImportReport{}, domain.ErrMetadataUnavailable
	}
	repository, ok := j.importRepository.(CatalogImportRepository)
	if !ok {
		return domain.CatalogImportReport{}, domain.ErrMetadataUnavailable
	}
	return repository.GetCatalogImportReport(ctx, actor, id)
}
