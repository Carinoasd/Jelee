package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// CatalogNamingRepository stores the per-library naming options (G21.3).
// Every call rechecks the live administrator.
type CatalogNamingRepository interface {
	GetCatalogNaming(context.Context, domain.Actor, string) (domain.CatalogNamingSettings, error)
	PutCatalogNaming(context.Context, domain.Actor, string, domain.CatalogNamingOptions) (domain.CatalogNamingSettings, error)
}

func (j *Jobs) catalogNaming() (CatalogNamingRepository, error) {
	if j == nil {
		return nil, domain.ErrMetadataUnavailable
	}
	repository, ok := j.repository.(CatalogNamingRepository)
	if !ok {
		return nil, domain.ErrMetadataUnavailable
	}
	return repository, nil
}

// CatalogNaming reads a library's naming options.
func (j *Jobs) CatalogNaming(ctx context.Context, actor domain.Actor, library string) (domain.CatalogNamingSettings, error) {
	if ctx == nil || !validTarget(actor, library) {
		return domain.CatalogNamingSettings{}, domain.ErrInvalid
	}
	repository, err := j.catalogNaming()
	if err != nil {
		return domain.CatalogNamingSettings{}, err
	}
	return repository.GetCatalogNaming(ctx, actor, library)
}

// PutCatalogNaming replaces a library's naming options. Nothing is planned
// again immediately; the next synchronisation sees the new parser version.
func (j *Jobs) PutCatalogNaming(ctx context.Context, actor domain.Actor, library string, o domain.CatalogNamingOptions) (domain.CatalogNamingSettings, error) {
	if ctx == nil || !validTarget(actor, library) {
		return domain.CatalogNamingSettings{}, domain.ErrInvalid
	}
	repository, err := j.catalogNaming()
	if err != nil {
		return domain.CatalogNamingSettings{}, err
	}
	return repository.PutCatalogNaming(ctx, actor, library, o)
}
