package app

import (
	"context"
	"errors"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type movieProviderFunc func(context.Context, int32, string) (domain.MovieCandidate, error)

func (f movieProviderFunc) SearchMovies(context.Context, domain.MovieSearchInput) ([]domain.MovieCandidate, error) {
	return nil, domain.ErrMetadataUnavailable
}

func (f movieProviderFunc) Movie(ctx context.Context, id int32, language string) (domain.MovieCandidate, error) {
	return f(ctx, id, language)
}

func TestMetadataValidatesAndMapsProviderErrors(t *testing.T) {
	if _, err := NewMetadata(nil); err != domain.ErrInvalid {
		t.Fatal("nil provider accepted")
	}
	calls := 0
	service, _ := NewMetadata(movieProviderFunc(func(context.Context, int32, string) (domain.MovieCandidate, error) {
		calls++
		return domain.MovieCandidate{}, nil
	}))
	for _, input := range []struct {
		id       int32
		language string
	}{{0, "en-US"}, {-1, "en-US"}, {1, "fr-FR"}} {
		if _, err := service.Movie(context.Background(), input.id, input.language); err != domain.ErrInvalid {
			t.Fatal("invalid request accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Movie(ctx, 1, "en-US"); err != context.Canceled || calls != 0 {
		t.Fatal("cancelled/invalid request reached provider")
	}
	for _, tc := range []struct{ source, want error }{{nil, nil}, {domain.ErrNotFound, domain.ErrNotFound}, {context.Canceled, context.Canceled}, {context.DeadlineExceeded, context.DeadlineExceeded}, {errors.New("secret://10.0.0.1"), domain.ErrMetadataUnavailable}} {
		service, _ = NewMetadata(movieProviderFunc(func(context.Context, int32, string) (domain.MovieCandidate, error) {
			return domain.MovieCandidate{ProviderID: 1}, tc.source
		}))
		movie, err := service.Movie(context.Background(), 1, "en-US")
		if !errors.Is(err, tc.want) || (err != nil && movie.ProviderID != 0) {
			t.Fatalf("result=%+v error=%v", movie, err)
		}
	}
}
