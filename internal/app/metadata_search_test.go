package app

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type searchProviderFunc func(context.Context, domain.MovieSearchInput) ([]domain.MovieCandidate, error)

func (f searchProviderFunc) Series(context.Context, int32, string) (domain.SeriesCandidate, error) {
	return domain.SeriesCandidate{}, domain.ErrNotFound
}
func (f searchProviderFunc) SearchSeries(context.Context, domain.SeriesSearchInput) ([]domain.SeriesCandidate, error) {
	return nil, domain.ErrMetadataUnavailable
}

func (f searchProviderFunc) SearchMovies(ctx context.Context, input domain.MovieSearchInput) ([]domain.MovieCandidate, error) {
	return f(ctx, input)
}
func (f searchProviderFunc) Movie(context.Context, int32, string) (domain.MovieCandidate, error) {
	return domain.MovieCandidate{}, domain.ErrNotFound
}

func TestMovieSearchTitleYearAndConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, title, original, date string
		year                        int
		titleMatch, yearMatch       bool
	}{
		{"exact", "Movie", "Original", "2024-02-29", 2024, true, true},
		{"wrong_year", "Movie", "Original", "1990-01-01", 2024, true, false},
		{"original_title", "翻譯", "mOvIe", "2024-02-29", 2024, true, true},
		{"different_title", "Other", "Other", "2024-02-29", 2024, false, true},
		{"no_year", "Movie", "Movie", "2024-02-29", 0, true, false},
		{"no_date", "Movie", "Movie", "", 2024, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, _ := NewMetadata(searchProviderFunc(func(_ context.Context, input domain.MovieSearchInput) ([]domain.MovieCandidate, error) {
				if input.Query != "Movie" {
					t.Fatal("query not normalized")
				}
				return []domain.MovieCandidate{{ProviderID: 12, Title: tc.title, OriginalTitle: tc.original, ReleaseDate: tc.date}}, nil
			}))
			result, err := service.SearchMovies(context.Background(), domain.MovieSearchInput{Query: "  Movie  ", Year: tc.year, Language: "en-US"})
			if err != nil || len(result.Candidates) != 1 {
				t.Fatal("search failed")
			}
			match := result.Candidates[0]
			if match.ExactTitle != tc.titleMatch || match.ExactYear != tc.yearMatch || !match.NeedsConfirmation {
				t.Fatal("unsafe matching decision")
			}
		})
	}
}

func TestMovieSearchOneHundredSyntheticTitles(t *testing.T) {
	// Isolated matching contract, not the full 100 movie/20 series acceptance.
	for i := 1; i <= 100; i++ {
		query := fmt.Sprintf("電影 %03d", i)
		service, _ := NewMetadata(searchProviderFunc(func(context.Context, domain.MovieSearchInput) ([]domain.MovieCandidate, error) {
			return []domain.MovieCandidate{{ProviderID: int32(i), Title: query, ReleaseDate: "2024-01-01"}}, nil
		}))
		result, err := service.SearchMovies(context.Background(), domain.MovieSearchInput{Query: query, Year: 2024, Language: "zh-TW"})
		if err != nil || len(result.Candidates) != 1 || !result.Candidates[0].ExactTitle || !result.Candidates[0].ExactYear || !result.Candidates[0].NeedsConfirmation {
			t.Fatalf("synthetic match %d failed", i)
		}
	}
}

func TestMovieSearchErrorAndEmptyResults(t *testing.T) {
	for _, tc := range []struct{ source, want error }{{nil, nil}, {errors.New("secret://10.0.0.1"), domain.ErrMetadataUnavailable}, {context.Canceled, context.Canceled}, {context.DeadlineExceeded, context.DeadlineExceeded}} {
		service, _ := NewMetadata(searchProviderFunc(func(context.Context, domain.MovieSearchInput) ([]domain.MovieCandidate, error) { return nil, tc.source }))
		result, err := service.SearchMovies(context.Background(), domain.MovieSearchInput{Query: "Movie", Language: "en-US"})
		if !errors.Is(err, tc.want) || (err == nil && result.Candidates == nil) {
			t.Fatal("error or empty result contract differs")
		}
	}
	service, _ := NewMetadata(searchProviderFunc(func(context.Context, domain.MovieSearchInput) ([]domain.MovieCandidate, error) {
		t.Fatal("invalid/cancelled input reached provider")
		return nil, nil
	}))
	if _, err := service.SearchMovies(context.Background(), domain.MovieSearchInput{}); err != domain.ErrInvalid {
		t.Fatal("invalid input accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.SearchMovies(ctx, domain.MovieSearchInput{Query: "Movie", Language: "en-US"}); err != context.Canceled {
		t.Fatal("cancelled input accepted")
	}
}
