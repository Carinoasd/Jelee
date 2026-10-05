package app

import (
	"context"
	"strings"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func metadataFallback[T any](ctx context.Context, language string, fetch func(context.Context, string) (T, error), merge func(*T, T, string) error, complete func(T) bool) (T, error) {
	var zero, result T
	budget, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for i, locale := range domain.MetadataFallbackLanguages(language) {
		if err := budget.Err(); err != nil {
			return zero, err
		}
		value, err := fetch(budget, locale)
		if err != nil {
			return zero, err
		}
		if i == 0 {
			result = value
		}
		if err := merge(&result, value, locale); err != nil {
			return zero, err
		}
		if complete(result) {
			break
		}
	}
	if err := budget.Err(); err != nil {
		return zero, err
	}
	return result, nil
}

func metadataSearchFallback[T any](ctx context.Context, input domain.MetadataSearchInput, fetch func(context.Context, domain.MetadataSearchInput) ([]T, error)) ([]T, string, error) {
	budget, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var result []T
	locale := input.Language
	for _, language := range domain.MetadataFallbackLanguages(input.Language) {
		if err := budget.Err(); err != nil {
			return nil, "", err
		}
		input.Language = language
		locale = language
		value, err := fetch(budget, input)
		if err != nil {
			return nil, "", err
		}
		if len(value) > 20 {
			return nil, "", domain.ErrMetadataUnavailable
		}
		result = value
		if len(result) > 0 {
			break
		}
	}
	if err := budget.Err(); err != nil {
		return nil, "", err
	}
	return result, locale, nil
}

func fillMetadataOverview(overview *string, source *domain.MetadataFieldSource, next string, language string, at time.Time) {
	if !hasMetadataOverview(*overview) {
		*overview = ""
		*source = domain.MetadataFieldSource{}
	}
	if *overview == "" && hasMetadataOverview(next) {
		*overview = next
		*source = domain.MetadataFieldSource{RequestedLanguage: language, FetchedAt: at}
		return
	}
	if *overview != "" && source.RequestedLanguage == "" {
		*source = domain.MetadataFieldSource{RequestedLanguage: language, FetchedAt: at}
	}
}

func hasMetadataOverview(value string) bool { return strings.TrimSpace(value) != "" }

func mergeMovieOverview(base *domain.MovieCandidate, next domain.MovieCandidate, language string) error {
	if base.ProviderID != next.ProviderID {
		return domain.ErrMetadataUnavailable
	}
	fillMetadataOverview(&base.Overview, &base.OverviewSource, next.Overview, language, next.FetchedAt)
	return nil
}
func mergeSeriesOverview(base *domain.SeriesCandidate, next domain.SeriesCandidate, language string) error {
	if base.ProviderID != next.ProviderID {
		return domain.ErrMetadataUnavailable
	}
	fillMetadataOverview(&base.Overview, &base.OverviewSource, next.Overview, language, next.FetchedAt)
	return nil
}
func mergeEpisodeOverview(base *domain.EpisodeCandidate, next domain.EpisodeCandidate, language string) error {
	if base.ProviderID != next.ProviderID || base.SeriesID != next.SeriesID || base.SeasonNumber != next.SeasonNumber || base.EpisodeNumber != next.EpisodeNumber {
		return domain.ErrMetadataUnavailable
	}
	fillMetadataOverview(&base.Overview, &base.OverviewSource, next.Overview, language, next.FetchedAt)
	return nil
}
func mergeSeasonOverview(base *domain.SeasonCandidate, next domain.SeasonCandidate, language string) error {
	if base.ProviderID != next.ProviderID || base.SeriesID != next.SeriesID || base.SeasonNumber != next.SeasonNumber {
		return domain.ErrMetadataUnavailable
	}
	fillMetadataOverview(&base.Overview, &base.OverviewSource, next.Overview, language, next.FetchedAt)
	byNumber := make(map[int32]domain.EpisodeCandidate, len(next.Episodes))
	for _, episode := range next.Episodes {
		byNumber[episode.EpisodeNumber] = episode
	}
	for i := range base.Episodes {
		value := byNumber[base.Episodes[i].EpisodeNumber]
		if value.ProviderID != base.Episodes[i].ProviderID || value.SeriesID != base.Episodes[i].SeriesID || value.SeasonNumber != base.Episodes[i].SeasonNumber {
			continue
		}
		fillMetadataOverview(&base.Episodes[i].Overview, &base.Episodes[i].OverviewSource, value.Overview, language, value.FetchedAt)
	}
	return nil
}
func seasonOverviewComplete(value domain.SeasonCandidate) bool {
	if !hasMetadataOverview(value.Overview) {
		return false
	}
	for _, episode := range value.Episodes {
		if !hasMetadataOverview(episode.Overview) {
			return false
		}
	}
	return true
}
