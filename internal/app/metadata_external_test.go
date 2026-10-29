package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type testExternalProvider struct {
	movieProviderFunc
	find func(context.Context, string, string, string) (domain.ExternalIDMatches, error)
}

func (p testExternalProvider) FindByExternalID(ctx context.Context, source, id, language string) (domain.ExternalIDMatches, error) {
	return p.find(ctx, source, id, language)
}

func TestFindByExternalIDConfirmationSourcesAndOwnership(t *testing.T) {
	at := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	stored := domain.ExternalIDMatches{ExternalSource: domain.ExternalSourceIMDb, ExternalID: "tt0137523", Language: "ja-JP", FetchedAt: at,
		Movies: []domain.MovieCandidate{{ProviderID: 550, Title: "ファイト・クラブ", Overview: "概要", Language: "ja-JP", FetchedAt: at}},
		Series: []domain.SeriesCandidate{{ProviderID: 1399, Title: "Series", Overview: "  ", Language: "ja-JP", FetchedAt: at}}}
	calls := 0
	service, _ := NewMetadata(testExternalProvider{find: func(_ context.Context, source, id, language string) (domain.ExternalIDMatches, error) {
		calls++
		if source != domain.ExternalSourceIMDb || id != "tt0137523" || language != "ja-JP" {
			t.Fatal("find arguments differ")
		}
		return stored, nil
	}})
	value, err := service.FindByExternalID(context.Background(), domain.ExternalSourceIMDb, "tt0137523", "ja-JP")
	if err != nil || !value.NeedsConfirmation || len(value.Movies) != 1 || len(value.Series) != 1 {
		t.Fatalf("value=%+v error=%v", value, err)
	}
	if value.Movies[0].OverviewSource != (domain.MetadataFieldSource{RequestedLanguage: "ja-JP", FetchedAt: at}) {
		t.Fatal("overview source missing")
	}
	if value.Series[0].Overview != "" || value.Series[0].OverviewSource != (domain.MetadataFieldSource{}) {
		t.Fatal("blank overview was reported as provider text")
	}
	value.Movies[0].Title = "changed"
	if stored.Movies[0].Title != "ファイト・クラブ" || stored.NeedsConfirmation || stored.Movies[0].OverviewSource.RequestedLanguage != "" {
		t.Fatal("application mutated provider value")
	}
	for _, tc := range []struct{ source, id, language string }{
		{"tmdb_id", "550", "en-US"}, {domain.ExternalSourceIMDb, "nm0000093", "en-US"}, {domain.ExternalSourceTVDB, "0", "en-US"},
		{domain.ExternalSourceWikidata, "Q0", "en-US"}, {domain.ExternalSourceIMDb, "tt0137523", "fr-FR"}, {domain.ExternalSourceIMDb, "tt0137523/../x", "en-US"},
	} {
		if _, err := service.FindByExternalID(context.Background(), tc.source, tc.id, tc.language); err != domain.ErrInvalid {
			t.Fatalf("invalid request accepted %+v", tc)
		}
	}
	if calls != 1 {
		t.Fatal("invalid request reached the provider")
	}
}

func TestFindByExternalIDRejectsMismatchedProviderAnswersAndHidesErrors(t *testing.T) {
	good := domain.ExternalIDMatches{ExternalSource: domain.ExternalSourceTVDB, ExternalID: "81189", Language: "en-US", Movies: []domain.MovieCandidate{}, Series: []domain.SeriesCandidate{}}
	for name, mutate := range map[string]func(*domain.ExternalIDMatches){
		"source":   func(v *domain.ExternalIDMatches) { v.ExternalSource = domain.ExternalSourceIMDb },
		"id":       func(v *domain.ExternalIDMatches) { v.ExternalID = "81190" },
		"language": func(v *domain.ExternalIDMatches) { v.Language = "zh-CN" },
		"too_many": func(v *domain.ExternalIDMatches) {
			v.Movies = make([]domain.MovieCandidate, domain.MaxExternalIDResults+1)
		},
		"zero_id": func(v *domain.ExternalIDMatches) { v.Series = []domain.SeriesCandidate{{}} },
	} {
		value := domain.CloneExternalIDMatches(good)
		mutate(&value)
		service, _ := NewMetadata(testExternalProvider{find: func(context.Context, string, string, string) (domain.ExternalIDMatches, error) { return value, nil }})
		if _, err := service.FindByExternalID(context.Background(), domain.ExternalSourceTVDB, "81189", "en-US"); err != domain.ErrMetadataUnavailable {
			t.Fatalf("%s: mismatched answer accepted: %v", name, err)
		}
	}
	for _, tc := range []struct{ source, want error }{{context.Canceled, context.Canceled}, {context.DeadlineExceeded, context.DeadlineExceeded}, {errors.New("https://secret@10.0.0.1/?api_key=x"), domain.ErrMetadataUnavailable}} {
		service, _ := NewMetadata(testExternalProvider{find: func(context.Context, string, string, string) (domain.ExternalIDMatches, error) {
			return domain.ExternalIDMatches{}, tc.source
		}})
		if _, err := service.FindByExternalID(context.Background(), domain.ExternalSourceTVDB, "81189", "en-US"); err != tc.want {
			t.Fatal("unsafe find error")
		}
	}
	// A provider without the capability is unavailable, not a panic.
	service, _ := NewMetadata(movieProviderFunc(nil))
	if _, err := service.FindByExternalID(context.Background(), domain.ExternalSourceTVDB, "81189", "en-US"); err != domain.ErrMetadataUnavailable {
		t.Fatal("missing capability not reported")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.FindByExternalID(ctx, domain.ExternalSourceTVDB, "81189", "en-US"); err != context.Canceled {
		t.Fatal("cancelled request accepted")
	}
}

type testEpisodeImageProvider struct {
	movieProviderFunc
	images func(context.Context, int32, int32, int32, []string) (domain.MetadataImages, error)
}

func (p testEpisodeImageProvider) EpisodeImages(ctx context.Context, series, season, episode int32, languages []string) (domain.MetadataImages, error) {
	return p.images(ctx, series, season, episode, languages)
}

func TestEpisodeImagesPreferenceOrderingAndTupleValidation(t *testing.T) {
	season := int32(1)
	stored := domain.MetadataImages{Resource: "episode", ProviderID: 63056, SeriesID: 1399, SeasonNumber: &season, EpisodeNumber: 2, ImageLanguages: []string{"ja", "null"}, Candidates: []domain.MetadataImageCandidate{
		{Kind: "still", Language: "null", FilePath: "/n.jpg", VoteAverage: 9},
		{Kind: "still", Language: "fr", FilePath: "/fr.jpg", VoteAverage: 10},
		{Kind: "still", Language: "ja", FilePath: "/j.jpg", VoteAverage: 1},
	}}
	service, _ := NewMetadata(testEpisodeImageProvider{images: func(_ context.Context, _, _, _ int32, languages []string) (domain.MetadataImages, error) {
		languages[0] = "en"
		return stored, nil
	}})
	languages := []string{"ja", "null"}
	value, err := service.EpisodeImages(context.Background(), 1399, 1, 2, languages)
	if err != nil || len(value.Candidates) != 2 || value.Candidates[0].FilePath != "/j.jpg" || value.Candidates[1].FilePath != "/n.jpg" || !value.Candidates[0].NeedsConfirmation || !value.Candidates[1].NeedsConfirmation {
		t.Fatalf("value=%+v error=%v", value, err)
	}
	*value.SeasonNumber = 7
	if season != 1 || languages[0] != "ja" || stored.Candidates[0].NeedsConfirmation {
		t.Fatal("application mutated provider or caller values")
	}
	for name, mutate := range map[string]func(*domain.MetadataImages){
		"resource":  func(v *domain.MetadataImages) { v.Resource = "season" },
		"series":    func(v *domain.MetadataImages) { v.SeriesID = 1 },
		"season":    func(v *domain.MetadataImages) { other := int32(2); v.SeasonNumber = &other },
		"no_season": func(v *domain.MetadataImages) { v.SeasonNumber = nil },
		"episode":   func(v *domain.MetadataImages) { v.EpisodeNumber = 3 },
		"provider":  func(v *domain.MetadataImages) { v.ProviderID = 0 },
		"languages": func(v *domain.MetadataImages) { v.ImageLanguages = []string{"null", "ja"} },
		"kind": func(v *domain.MetadataImages) {
			v.Candidates = []domain.MetadataImageCandidate{{Kind: "poster", Language: "ja"}}
		},
	} {
		value := domain.CloneMetadataImages(stored)
		mutate(&value)
		service, _ := NewMetadata(testEpisodeImageProvider{images: func(context.Context, int32, int32, int32, []string) (domain.MetadataImages, error) { return value, nil }})
		if _, err := service.EpisodeImages(context.Background(), 1399, 1, 2, []string{"ja", "null"}); err != domain.ErrMetadataUnavailable {
			t.Fatalf("%s: mismatched answer accepted", name)
		}
	}
	for _, target := range [][3]int32{{0, 1, 1}, {1, -1, 0}, {1, 0, -1}} {
		if _, err := service.EpisodeImages(context.Background(), target[0], target[1], target[2], []string{"ja"}); err != domain.ErrInvalid {
			t.Fatal("invalid target accepted")
		}
	}
	if _, err := service.EpisodeImages(context.Background(), 1, 1, 0, []string{"ja", "ja"}); err != domain.ErrInvalid {
		t.Fatal("invalid preference accepted")
	}
	missing, _ := NewMetadata(movieProviderFunc(nil))
	if _, err := missing.EpisodeImages(context.Background(), 1, 1, 0, []string{"ja"}); err != domain.ErrMetadataUnavailable {
		t.Fatal("missing capability not reported")
	}
	failing, _ := NewMetadata(testEpisodeImageProvider{images: func(context.Context, int32, int32, int32, []string) (domain.MetadataImages, error) {
		return domain.MetadataImages{}, errors.New("secret upstream detail")
	}})
	if _, err := failing.EpisodeImages(context.Background(), 1, 1, 0, []string{"ja"}); err != domain.ErrMetadataUnavailable {
		t.Fatal("unsafe image error")
	}
}
