package app

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type MovieProvider interface {
	Movie(context.Context, int32, string) (domain.MovieCandidate, error)
	SearchMovies(context.Context, domain.MovieSearchInput) ([]domain.MovieCandidate, error)
}

type MetadataProvider interface {
	MovieProvider
	Series(context.Context, int32, string) (domain.SeriesCandidate, error)
	SearchSeries(context.Context, domain.SeriesSearchInput) ([]domain.SeriesCandidate, error)
}

// SearchMovies reports simple title/year comparisons for review. It never
// selects a result or authorizes an automatic metadata write.
func (m *Metadata) SearchMovies(ctx context.Context, input domain.MovieSearchInput) (domain.MovieMatches, error) {
	if err := ctx.Err(); err != nil {
		return domain.MovieMatches{}, err
	}
	input, err := domain.NormalizeMetadataSearch(input)
	if err != nil {
		return domain.MovieMatches{}, err
	}
	candidates, err := m.provider.SearchMovies(ctx, input)
	if err != nil {
		for _, safe := range []error{context.Canceled, context.DeadlineExceeded} {
			if errors.Is(err, safe) {
				return domain.MovieMatches{}, safe
			}
		}
		return domain.MovieMatches{}, domain.ErrMetadataUnavailable
	}
	if len(candidates) > 20 {
		return domain.MovieMatches{}, domain.ErrMetadataUnavailable
	}
	result := domain.MovieMatches{MovieSearchInput: input, Candidates: make([]domain.MovieMatch, 0, len(candidates))}
	for _, movie := range candidates {
		match := domain.MovieMatch{Movie: movie, NeedsConfirmation: true, ExactTitle: strings.EqualFold(strings.TrimSpace(movie.Title), input.Query) || strings.EqualFold(strings.TrimSpace(movie.OriginalTitle), input.Query)}
		if input.Year != 0 && len(movie.ReleaseDate) == 10 {
			match.ExactYear = movie.ReleaseDate[:4] == strconv.Itoa(input.Year)
		}
		result.Candidates = append(result.Candidates, match)
	}
	return result, nil
}

type Metadata struct{ provider MetadataProvider }

func NewMetadata(provider MetadataProvider) (*Metadata, error) {
	if provider == nil {
		return nil, domain.ErrInvalid
	}
	return &Metadata{provider: provider}, nil
}

func (m *Metadata) Movie(ctx context.Context, id int32, language string) (domain.MovieCandidate, error) {
	if err := ctx.Err(); err != nil {
		return domain.MovieCandidate{}, err
	}
	if id <= 0 || !domain.ValidMetadataLanguage(language) {
		return domain.MovieCandidate{}, domain.ErrInvalid
	}
	result, err := m.provider.Movie(ctx, id, language)
	if err == nil {
		return result, nil
	}
	for _, safe := range []error{domain.ErrNotFound, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, safe) {
			return domain.MovieCandidate{}, safe
		}
	}
	return domain.MovieCandidate{}, domain.ErrMetadataUnavailable
}
