package app

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func (m *Metadata) Series(ctx context.Context, id int32, language string) (domain.SeriesCandidate, error) {
	if err := ctx.Err(); err != nil {
		return domain.SeriesCandidate{}, err
	}
	if id <= 0 || !domain.ValidMetadataLanguage(language) {
		return domain.SeriesCandidate{}, domain.ErrInvalid
	}
	result, err := m.provider.Series(ctx, id, language)
	if err == nil {
		return result, nil
	}
	for _, safe := range []error{domain.ErrNotFound, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, safe) {
			return domain.SeriesCandidate{}, safe
		}
	}
	return domain.SeriesCandidate{}, domain.ErrMetadataUnavailable
}

func (m *Metadata) SearchSeries(ctx context.Context, input domain.SeriesSearchInput) (domain.SeriesMatches, error) {
	if err := ctx.Err(); err != nil {
		return domain.SeriesMatches{}, err
	}
	input, err := domain.NormalizeMetadataSearch(input)
	if err != nil {
		return domain.SeriesMatches{}, err
	}
	candidates, err := m.provider.SearchSeries(ctx, input)
	if err != nil {
		for _, safe := range []error{context.Canceled, context.DeadlineExceeded} {
			if errors.Is(err, safe) {
				return domain.SeriesMatches{}, safe
			}
		}
		return domain.SeriesMatches{}, domain.ErrMetadataUnavailable
	}
	if len(candidates) > 20 {
		return domain.SeriesMatches{}, domain.ErrMetadataUnavailable
	}
	result := domain.SeriesMatches{SeriesSearchInput: input, Candidates: make([]domain.SeriesMatch, 0, len(candidates))}
	for _, series := range candidates {
		match := domain.SeriesMatch{Series: series, NeedsConfirmation: true, ExactTitle: strings.EqualFold(strings.TrimSpace(series.Title), input.Query) || strings.EqualFold(strings.TrimSpace(series.OriginalTitle), input.Query)}
		if input.Year != 0 && len(series.FirstAirDate) == 10 {
			match.ExactYear = series.FirstAirDate[:4] == strconv.Itoa(input.Year)
		}
		result.Candidates = append(result.Candidates, match)
	}
	return result, nil
}
