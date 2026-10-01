package app

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func (m *Metadata) Season(ctx context.Context, seriesID, seasonNumber int32, language string) (domain.SeasonCandidate, error) {
	if err := ctx.Err(); err != nil {
		return domain.SeasonCandidate{}, err
	}
	if !domain.ValidSeasonRequest(seriesID, seasonNumber, language) {
		return domain.SeasonCandidate{}, domain.ErrInvalid
	}
	value, err := metadataFallback(ctx, language, func(c context.Context, l string) (domain.SeasonCandidate, error) {
		value, err := m.provider.Season(c, seriesID, seasonNumber, l)
		copyValue := make([]domain.EpisodeCandidate, len(value.Episodes))
		copy(copyValue, value.Episodes)
		value.Episodes = copyValue
		return value, err
	}, mergeSeasonOverview, seasonOverviewComplete)
	if err != nil {
		return domain.SeasonCandidate{}, safeEpisodeError(err)
	}
	return value, nil
}
func (m *Metadata) Episode(ctx context.Context, seriesID, seasonNumber, episodeNumber int32, language string) (domain.EpisodeCandidate, error) {
	if err := ctx.Err(); err != nil {
		return domain.EpisodeCandidate{}, err
	}
	if !domain.ValidEpisodeRequest(seriesID, seasonNumber, episodeNumber, language) {
		return domain.EpisodeCandidate{}, domain.ErrInvalid
	}
	value, err := metadataFallback(ctx, language, func(c context.Context, l string) (domain.EpisodeCandidate, error) {
		return m.provider.Episode(c, seriesID, seasonNumber, episodeNumber, l)
	}, mergeEpisodeOverview, func(v domain.EpisodeCandidate) bool { return hasMetadataOverview(v.Overview) })
	if err != nil {
		return domain.EpisodeCandidate{}, safeEpisodeError(err)
	}
	return value, nil
}
func safeEpisodeError(err error) error {
	for _, safe := range []error{domain.ErrNotFound, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	return domain.ErrMetadataUnavailable
}
