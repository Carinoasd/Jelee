package app

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type testSeriesProvider struct {
	movieProviderFunc
	search func(context.Context, domain.SeriesSearchInput) ([]domain.SeriesCandidate, error)
	detail func(context.Context, int32, string) (domain.SeriesCandidate, error)
}

func (p testSeriesProvider) Series(ctx context.Context, id int32, language string) (domain.SeriesCandidate, error) {
	return p.detail(ctx, id, language)
}
func (p testSeriesProvider) SearchSeries(ctx context.Context, input domain.SeriesSearchInput) ([]domain.SeriesCandidate, error) {
	return p.search(ctx, input)
}

func TestSeriesTwentySyntheticMatchesAndConfirmation(t *testing.T) {
	var candidates []domain.SeriesCandidate
	for id := int32(1); id <= 20; id++ {
		candidates = append(candidates, domain.SeriesCandidate{ProviderID: id, Title: fmt.Sprintf("劇集 %02d", id), OriginalTitle: "Series", FirstAirDate: "2024-01-01"})
	}
	service, _ := NewMetadata(testSeriesProvider{search: func(context.Context, domain.SeriesSearchInput) ([]domain.SeriesCandidate, error) {
		return candidates, nil
	}})
	result, err := service.SearchSeries(context.Background(), domain.SeriesSearchInput{Query: "series", Year: 2024, Language: "zh-TW"})
	if err != nil || len(result.Candidates) != 20 {
		t.Fatal("series query failed")
	}
	for _, match := range result.Candidates {
		if !match.ExactTitle || !match.ExactYear || !match.NeedsConfirmation {
			t.Fatal("automatic series confirmation")
		}
	}
	result, err = service.SearchSeries(context.Background(), domain.SeriesSearchInput{Query: "Unrelated", Year: 1990, Language: "en-US"})
	if err != nil {
		t.Fatal(err)
	}
	for _, match := range result.Candidates {
		if match.ExactTitle || match.ExactYear || !match.NeedsConfirmation {
			t.Fatal("mismatched series confirmed")
		}
	}
}

func TestSeriesServiceValidationAndSafeErrors(t *testing.T) {
	for _, tc := range []struct{ source, want error }{{nil, nil}, {domain.ErrNotFound, domain.ErrNotFound}, {context.Canceled, context.Canceled}, {context.DeadlineExceeded, context.DeadlineExceeded}, {errors.New("secret://10.0.0.1"), domain.ErrMetadataUnavailable}} {
		service, _ := NewMetadata(testSeriesProvider{detail: func(context.Context, int32, string) (domain.SeriesCandidate, error) {
			return domain.SeriesCandidate{ProviderID: 12}, tc.source
		}, search: func(context.Context, domain.SeriesSearchInput) ([]domain.SeriesCandidate, error) {
			return nil, tc.source
		}})
		value, err := service.Series(context.Background(), 12, "en-US")
		if !errors.Is(err, tc.want) || (err != nil && value.ProviderID != 0) {
			t.Fatal("unsafe series error")
		}
		result, err := service.SearchSeries(context.Background(), domain.SeriesSearchInput{Query: "Series", Language: "en-US"})
		want := tc.want
		if want == domain.ErrNotFound {
			want = domain.ErrMetadataUnavailable
		}
		if !errors.Is(err, want) || (err == nil && result.Candidates == nil) {
			t.Fatal("unsafe search error or null empty results")
		}
	}
	service, _ := NewMetadata(testSeriesProvider{detail: func(context.Context, int32, string) (domain.SeriesCandidate, error) {
		t.Fatal("invalid request reached provider")
		return domain.SeriesCandidate{}, nil
	}, search: func(context.Context, domain.SeriesSearchInput) ([]domain.SeriesCandidate, error) {
		t.Fatal("invalid search reached provider")
		return nil, nil
	}})
	if _, err := service.Series(context.Background(), 0, "en-US"); err != domain.ErrInvalid {
		t.Fatal("invalid id accepted")
	}
	if _, err := service.SearchSeries(context.Background(), domain.SeriesSearchInput{}); err != domain.ErrInvalid {
		t.Fatal("invalid search accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Series(ctx, 12, "en-US"); err != context.Canceled {
		t.Fatal("cancelled series accepted")
	}
	if _, err := service.SearchSeries(ctx, domain.SeriesSearchInput{Query: "Series", Language: "en-US"}); err != context.Canceled {
		t.Fatal("cancelled search accepted")
	}
}
