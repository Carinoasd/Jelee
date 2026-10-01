package app

import (
	"context"
	"errors"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type MovieProvider interface {
	Movie(context.Context, int32, string) (domain.MovieCandidate, error)
}

type Metadata struct{ provider MovieProvider }

func NewMetadata(provider MovieProvider) (*Metadata, error) {
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
