package postgres

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

var _ app.CatalogNamingRepository = (*Store)(nil)

// GetCatalogNaming reads a library's naming options (G21.3).
func (s *Store) GetCatalogNaming(ctx context.Context, actor domain.Actor, library string) (domain.CatalogNamingSettings, error) {
	if ctx == nil || !domain.ValidID(library) {
		return domain.CatalogNamingSettings{}, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.CatalogNamingSettings{}, err
	}
	defer tx.Rollback(ctx)
	o, err := readCatalogNaming(ctx, tx, library)
	if err != nil {
		return domain.CatalogNamingSettings{}, err
	}
	return catalogNamingSettings(library, o), storageError(tx.Commit(ctx))
}

// PutCatalogNaming replaces a library's naming options. It takes the same
// library row lock as a synchronisation batch, so a batch plans with either
// the old or the new options, never a mix; the new parser version makes the
// next synchronisation plan every tracked and pending file again.
func (s *Store) PutCatalogNaming(ctx context.Context, actor domain.Actor, library string, o domain.CatalogNamingOptions) (domain.CatalogNamingSettings, error) {
	if ctx == nil || !domain.ValidID(library) {
		return domain.CatalogNamingSettings{}, domain.ErrInvalid
	}
	tx, err := s.authorizedJobs(ctx, actor)
	if err != nil {
		return domain.CatalogNamingSettings{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = lockLibraryBaseline(ctx, tx, library); err != nil {
		return domain.CatalogNamingSettings{}, err
	}
	before, err := readCatalogNaming(ctx, tx, library)
	if err != nil {
		return domain.CatalogNamingSettings{}, err
	}
	if before != o {
		if _, err = tx.Exec(ctx, `UPDATE libraries SET naming_date_episodes=$2,naming_absolute_episodes=$3 WHERE id=$1::uuid`, library, o.DateEpisodes, o.AbsoluteEpisodes); err != nil {
			return domain.CatalogNamingSettings{}, storageError(err)
		}
		if err = auditAccount(ctx, tx, actor, "library.catalog_naming_changed", library, before, o); err != nil {
			return domain.CatalogNamingSettings{}, err
		}
	}
	return catalogNamingSettings(library, o), storageError(tx.Commit(ctx))
}

func catalogNamingSettings(library string, o domain.CatalogNamingOptions) domain.CatalogNamingSettings {
	return domain.CatalogNamingSettings{LibraryID: library, CatalogNamingOptions: o, ParserVersion: domain.CatalogSyncParserVersionFor(o)}
}
