package app

import (
	"context"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type MetadataApplyRepository interface {
	ApplyTMDBMetadata(context.Context, domain.Actor, string, int64, domain.TMDBMetadataUpdate) (domain.MetadataApplyResult, error)
}

type MetadataNFOFusionRepository interface {
	ApplyTMDBWithNFO(context.Context, domain.Actor, domain.NFOItemScope, domain.NFOItemFields, domain.TMDBMetadataUpdate) (domain.MetadataApplyResult, error)
}

func (m *Metadata) ApplyTMDB(ctx context.Context, actor domain.Actor, item string, input domain.TMDBMetadataApplyInput) (domain.MetadataApplyResult, error) {
	if err := ctx.Err(); err != nil {
		return domain.MetadataApplyResult{}, err
	}
	if !domain.ValidID(actor.UserID) || !domain.ValidID(actor.SessionID) {
		return domain.MetadataApplyResult{}, domain.ErrUnauthenticated
	}
	if !domain.ValidTMDBMetadataInput(item, input) {
		return domain.MetadataApplyResult{}, domain.ErrInvalid
	}
	if !m.HasProvider() || !m.HasItemMetadata() {
		return domain.MetadataApplyResult{}, domain.ErrMetadataUnavailable
	}
	repository, ok := m.items.(MetadataApplyRepository)
	if !ok {
		return domain.MetadataApplyResult{}, domain.ErrMetadataUnavailable
	}
	before, err := m.ItemFields(ctx, actor, item)
	if err != nil {
		return domain.MetadataApplyResult{}, err
	}
	if before.ItemID != item || !domain.ValidID(before.LibraryID) {
		return domain.MetadataApplyResult{}, domain.ErrMetadataUnavailable
	}
	if before.NFOMode != domain.NFOModeOff && before.NFOMode != domain.NFOModeReadOnly {
		return domain.MetadataApplyResult{}, domain.ErrMetadataUnavailable
	}
	if before.Revision != input.ExpectedRevision {
		return domain.MetadataApplyResult{}, domain.ErrConflict
	}
	if !domain.MetadataResourceMatchesKind(input.Resource, before.Kind) {
		return domain.MetadataApplyResult{}, domain.ErrInvalid
	}
	var nfoScope domain.NFOItemScope
	var nfoFields domain.NFOItemSelection
	var fusion MetadataNFOFusionRepository
	if before.NFOMode == domain.NFOModeReadOnly {
		var ok bool
		fusion, ok = m.items.(MetadataNFOFusionRepository)
		if !ok || m.nfoFields == nil {
			return domain.MetadataApplyResult{}, domain.ErrMetadataUnavailable
		}
		nfoScope, nfoFields, err = m.readItemNFO(ctx, actor, item, input.ExpectedRevision)
		if err != nil {
			return domain.MetadataApplyResult{}, err
		}
		if nfoScope.LibraryID != before.LibraryID || nfoScope.Kind != before.Kind {
			return domain.MetadataApplyResult{}, domain.ErrConflict
		}
	}
	language := input.Language
	if language == "" {
		preferences, err := m.LibraryPreferences(ctx, actor, before.LibraryID)
		if err != nil {
			return domain.MetadataApplyResult{}, err
		}
		language = preferences.Language
	}
	var id int32
	var source, sourceURL, actualLanguage, title, original, overview, date string
	var fetched time.Time
	var overviewSource domain.MetadataFieldSource
	if input.Resource == "movie" {
		candidate, err := m.Movie(ctx, input.ProviderID, language)
		if err != nil {
			return domain.MetadataApplyResult{}, err
		}
		id, source, sourceURL, actualLanguage = candidate.ProviderID, candidate.Source, candidate.SourceURL, candidate.Language
		title, original, overview, date = candidate.Title, candidate.OriginalTitle, candidate.Overview, candidate.ReleaseDate
		fetched, overviewSource = candidate.FetchedAt, candidate.OverviewSource
	} else {
		candidate, err := m.Series(ctx, input.ProviderID, language)
		if err != nil {
			return domain.MetadataApplyResult{}, err
		}
		id, source, sourceURL, actualLanguage = candidate.ProviderID, candidate.Source, candidate.SourceURL, candidate.Language
		title, original, overview, date = candidate.Title, candidate.OriginalTitle, candidate.Overview, candidate.FirstAirDate
		fetched, overviewSource = candidate.FetchedAt, candidate.OverviewSource
	}
	origin := domain.MetadataProviderOrigin{Resource: input.Resource, ProviderID: id, SourceURL: sourceURL, RequestedLanguage: actualLanguage, FetchedAt: fetched.UTC()}
	if id != input.ProviderID || source != "TMDB" || actualLanguage != language || !domain.ValidMetadataProviderOrigin(origin) {
		return domain.MetadataApplyResult{}, domain.ErrMetadataUnavailable
	}
	update := domain.TMDBMetadataUpdate{Resource: input.Resource, ProviderID: input.ProviderID, ReplaceExistingTitle: input.ReplaceExistingTitle, Fields: []domain.MetadataProviderField{}}
	for _, value := range []struct{ field, value string }{{"title", title}, {"originalTitle", original}, {"overview", overview}, {"date", date}} {
		if strings.TrimSpace(value.value) == "" && value.field != "title" {
			continue
		}
		fieldOrigin := origin
		if value.field == "overview" {
			fieldOrigin.RequestedLanguage = overviewSource.RequestedLanguage
			fieldOrigin.FetchedAt = overviewSource.FetchedAt.UTC()
		}
		update.Fields = append(update.Fields, domain.MetadataProviderField{Field: value.field, Value: value.value, Origin: fieldOrigin})
	}
	if !domain.ValidTMDBMetadataUpdate(update) {
		return domain.MetadataApplyResult{}, domain.ErrMetadataUnavailable
	}
	if err := ctx.Err(); err != nil {
		return domain.MetadataApplyResult{}, err
	}
	var result domain.MetadataApplyResult
	if fusion != nil {
		var last domain.NFOItemSelection
		last, err = m.rereadItemNFO(ctx, nfoScope, nfoFields)
		if err != nil {
			return domain.MetadataApplyResult{}, err
		}
		result, err = fusion.ApplyTMDBWithNFO(ctx, actor, nfoScope, last.Fields, update)
	} else {
		result, err = repository.ApplyTMDBMetadata(ctx, actor, item, input.ExpectedRevision, update)
	}
	return domain.CloneMetadataApplyResult(result), err
}
