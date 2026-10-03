package app

import (
	"context"
	"errors"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type testEpisodeProvider struct {
	movieProviderFunc
	season  func(context.Context, int32, int32, string) (domain.SeasonCandidate, error)
	episode func(context.Context, int32, int32, int32, string) (domain.EpisodeCandidate, error)
}

func (p testEpisodeProvider) Season(ctx context.Context, id, season int32, language string) (domain.SeasonCandidate, error) {
	return p.season(ctx, id, season, language)
}
func (p testEpisodeProvider) Episode(ctx context.Context, id, season, episode int32, language string) (domain.EpisodeCandidate, error) {
	return p.episode(ctx, id, season, episode, language)
}

func TestSeasonEpisodeApplicationValidationAndSafeErrors(t *testing.T) {
	for _, tc := range []struct{ source, want error }{{nil, nil}, {domain.ErrNotFound, domain.ErrNotFound}, {context.Canceled, context.Canceled}, {context.DeadlineExceeded, context.DeadlineExceeded}, {errors.New("secret://10.0.0.1"), domain.ErrMetadataUnavailable}} {
		service, _ := NewMetadata(testEpisodeProvider{season: func(_ context.Context, id, season int32, language string) (domain.SeasonCandidate, error) {
			if id != 12 || season != 0 || language != "zh-TW" {
				t.Fatal("season arguments lost")
			}
			return domain.SeasonCandidate{ProviderID: 500, Overview: "Summary"}, tc.source
		}, episode: func(_ context.Context, id, season, episode int32, language string) (domain.EpisodeCandidate, error) {
			if id != 12 || season != 0 || episode != 1 || language != "zh-TW" {
				t.Fatal("episode arguments lost")
			}
			return domain.EpisodeCandidate{ProviderID: 900, Overview: "Summary"}, tc.source
		}})
		season, err := service.Season(context.Background(), 12, 0, "zh-TW")
		if !errors.Is(err, tc.want) || (err != nil && season.ProviderID != 0) {
			t.Fatal("unsafe season error")
		}
		episode, err := service.Episode(context.Background(), 12, 0, 1, "zh-TW")
		if !errors.Is(err, tc.want) || (err != nil && episode.ProviderID != 0) {
			t.Fatal("unsafe episode error")
		}
	}
	service, _ := NewMetadata(testEpisodeProvider{season: func(context.Context, int32, int32, string) (domain.SeasonCandidate, error) {
		t.Fatal("invalid season reached provider")
		return domain.SeasonCandidate{}, nil
	}, episode: func(context.Context, int32, int32, int32, string) (domain.EpisodeCandidate, error) {
		t.Fatal("invalid episode reached provider")
		return domain.EpisodeCandidate{}, nil
	}})
	if _, err := service.Season(context.Background(), 0, 0, "en-US"); err != domain.ErrInvalid {
		t.Fatal("invalid series accepted")
	}
	if _, err := service.Season(context.Background(), 12, -1, "en-US"); err != domain.ErrInvalid {
		t.Fatal("negative season accepted")
	}
	if _, err := service.Episode(context.Background(), 12, 0, 0, "en-US"); err != domain.ErrInvalid {
		t.Fatal("zero episode accepted")
	}
	if _, err := service.Episode(context.Background(), 12, 0, 1, "fr-FR"); err != domain.ErrInvalid {
		t.Fatal("unknown language accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Season(ctx, 12, 0, "en-US"); err != context.Canceled {
		t.Fatal("cancelled season accepted")
	}
	if _, err := service.Episode(ctx, 12, 0, 1, "en-US"); err != context.Canceled {
		t.Fatal("cancelled episode accepted")
	}
}
