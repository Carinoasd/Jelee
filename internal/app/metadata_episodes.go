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
	value, err := m.provider.Season(ctx, seriesID, seasonNumber, language)
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
	value, err := m.provider.Episode(ctx, seriesID, seasonNumber, episodeNumber, language)
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
