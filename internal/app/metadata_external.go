package app

import (
	"context"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// ExternalIDProvider resolves an IMDb, TVDB or Wikidata identifier to
// provider candidates.
type ExternalIDProvider interface {
	FindByExternalID(ctx context.Context, source, id, language string) (domain.ExternalIDMatches, error)
}

// FindByExternalID lists the movie and series candidates of one external
// identifier for administrator review. It never selects a candidate or
// writes metadata; every result needs confirmation.
func (m *Metadata) FindByExternalID(ctx context.Context, source, id, language string) (domain.ExternalIDMatches, error) {
	if err := ctx.Err(); err != nil {
		return domain.ExternalIDMatches{}, err
	}
	if !domain.ValidExternalIDRequest(source, id, language) {
		return domain.ExternalIDMatches{}, domain.ErrInvalid
	}
	provider, ok := m.provider.(ExternalIDProvider)
	if !ok {
		return domain.ExternalIDMatches{}, domain.ErrMetadataUnavailable
	}
	value, err := provider.FindByExternalID(ctx, source, id, language)
	if err != nil {
		return domain.ExternalIDMatches{}, safeEpisodeError(err)
	}
	if err := ctx.Err(); err != nil {
		return domain.ExternalIDMatches{}, err
	}
	if value.ExternalSource != source || value.ExternalID != id || value.Language != language || len(value.Movies) > domain.MaxExternalIDResults || len(value.Series) > domain.MaxExternalIDResults {
		return domain.ExternalIDMatches{}, domain.ErrMetadataUnavailable
	}
	value = domain.CloneExternalIDMatches(value)
	for i := range value.Movies {
		movie := &value.Movies[i]
		if movie.ProviderID <= 0 {
			return domain.ExternalIDMatches{}, domain.ErrMetadataUnavailable
		}
		fillMetadataOverview(&movie.Overview, &movie.OverviewSource, movie.Overview, language, movie.FetchedAt)
	}
	for i := range value.Series {
		series := &value.Series[i]
		if series.ProviderID <= 0 {
			return domain.ExternalIDMatches{}, domain.ErrMetadataUnavailable
		}
		fillMetadataOverview(&series.Overview, &series.OverviewSource, series.Overview, language, series.FetchedAt)
	}
	value.NeedsConfirmation = true
	return value, nil
}
