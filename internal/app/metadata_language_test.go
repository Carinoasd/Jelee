package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestMovieLanguageFallbackPreservesPrimaryFieldsAndBudget(t *testing.T) {
	var calls []string
	var deadline time.Time
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	service, _ := NewMetadata(movieProviderFunc(func(ctx context.Context, id int32, language string) (domain.MovieCandidate, error) {
		calls = append(calls, language)
		d, ok := ctx.Deadline()
		if !ok || (!deadline.IsZero() && !d.Equal(deadline)) {
			t.Fatal("fallback budget changed")
		}
		deadline = d
		v := domain.MovieCandidate{ProviderID: id, Title: language, Language: language, FetchedAt: at.Add(time.Duration(len(calls)) * time.Second), Overview: " \n\t"}
		if language == "en-US" {
			v.Overview = "English overview"
		}
		return v, nil
	}))
	v, err := service.Movie(context.Background(), 12, "zh-CN")
	if err != nil || !reflect.DeepEqual(calls, []string{"zh-CN", "zh-TW", "ja-JP", "en-US"}) {
		t.Fatalf("calls=%v error=%v", calls, err)
	}
	if v.Title != "zh-CN" || v.Language != "zh-CN" || !v.FetchedAt.Equal(at.Add(time.Second)) || v.Overview != "English overview" || v.OverviewSource.RequestedLanguage != "en-US" || !v.OverviewSource.FetchedAt.Equal(at.Add(4*time.Second)) {
		t.Fatalf("provenance=%+v", v)
	}
}

func TestMovieLanguageFallbackStopsAndDoesNotInventText(t *testing.T) {
	for _, overview := range []string{"Summary", " \n"} {
		calls := 0
		service, _ := NewMetadata(movieProviderFunc(func(_ context.Context, id int32, language string) (domain.MovieCandidate, error) {
			calls++
			return domain.MovieCandidate{ProviderID: id, Language: language, Overview: overview, FetchedAt: time.Now()}, nil
		}))
		v, err := service.Movie(context.Background(), 12, "zh-TW")
		if err != nil {
			t.Fatal(err)
		}
		if overview == "Summary" {
			if calls != 1 || v.OverviewSource.RequestedLanguage != "zh-TW" {
				t.Fatal("nonempty overview did not stop")
			}
		} else if calls != 3 || v.Overview != "" || v.OverviewSource != (domain.MetadataFieldSource{}) {
			t.Fatal("missing overview acquired false provenance")
		}
	}
}

func TestMovieLanguageFallbackErrorsAreTerminal(t *testing.T) {
	for _, failure := range []error{domain.ErrNotFound, context.Canceled, context.DeadlineExceeded, domain.ErrMetadataUnavailable} {
		calls := 0
		service, _ := NewMetadata(movieProviderFunc(func(context.Context, int32, string) (domain.MovieCandidate, error) {
			calls++
			return domain.MovieCandidate{}, failure
		}))
		if _, err := service.Movie(context.Background(), 12, "zh-CN"); !errors.Is(err, failure) || calls != 1 {
			t.Fatalf("error=%v calls=%d", err, calls)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	service, _ := NewMetadata(movieProviderFunc(func(context.Context, int32, string) (domain.MovieCandidate, error) {
		calls++
		cancel()
		return domain.MovieCandidate{ProviderID: 12}, nil
	}))
	if _, err := service.Movie(ctx, 12, "zh-CN"); err != context.Canceled || calls != 1 {
		t.Fatal("cancellation continued fallback")
	}
	service, _ = NewMetadata(movieProviderFunc(func(_ context.Context, _ int32, language string) (domain.MovieCandidate, error) {
		id := int32(12)
		if language == "en-US" {
			id = 13
		}
		return domain.MovieCandidate{ProviderID: id, Overview: map[string]string{"en-US": "Summary"}[language]}, nil
	}))
	if _, err := service.Movie(context.Background(), 12, "ja-JP"); err != domain.ErrMetadataUnavailable {
		t.Fatal("identity mismatch merged")
	}
}

func TestSeasonLanguageFallbackOwnsEpisodeSliceAndChecksIdentity(t *testing.T) {
	at := time.Now().UTC()
	primary := []domain.EpisodeCandidate{{ProviderID: 900, SeriesID: 12, SeasonNumber: 0, EpisodeNumber: 1, Title: "Primary", FetchedAt: at}}
	var calls []string
	service, _ := NewMetadata(testEpisodeProvider{season: func(_ context.Context, id, number int32, language string) (domain.SeasonCandidate, error) {
		calls = append(calls, language)
		v := domain.SeasonCandidate{ProviderID: 500, SeriesID: id, SeasonNumber: number, Language: language, FetchedAt: at, Episodes: primary}
		if language != "zh-TW" {
			v.Overview = "Season overview"
			v.Episodes = []domain.EpisodeCandidate{{ProviderID: 901, SeriesID: id, SeasonNumber: number, EpisodeNumber: 1, Overview: "Wrong identity", FetchedAt: at}}
			if language == "en-US" {
				v.Episodes[0].ProviderID = 900
				v.Episodes[0].Overview = "Episode overview"
			}
		}
		return v, nil
	}})
	v, err := service.Season(context.Background(), 12, 0, "zh-TW")
	if err != nil || !reflect.DeepEqual(calls, []string{"zh-TW", "ja-JP", "en-US"}) {
		t.Fatalf("calls=%v error=%v", calls, err)
	}
	if primary[0].Overview != "" || v.Episodes[0].Title != "Primary" || v.Episodes[0].Overview != "Episode overview" || v.OverviewSource.RequestedLanguage != "ja-JP" || v.Episodes[0].OverviewSource.RequestedLanguage != "en-US" {
		t.Fatalf("season=%+v", v)
	}
	v.Episodes[0].Title = "Changed"
	if primary[0].Title != "Primary" {
		t.Fatal("caller mutated provider slice")
	}
}

func TestEpisodeLanguageFallbackKeepsTuple(t *testing.T) {
	var calls []string
	service, _ := NewMetadata(testEpisodeProvider{episode: func(_ context.Context, id, season, episode int32, language string) (domain.EpisodeCandidate, error) {
		calls = append(calls, language)
		v := domain.EpisodeCandidate{ProviderID: 900, SeriesID: id, SeasonNumber: season, EpisodeNumber: episode, Title: language, Language: language, FetchedAt: time.Now()}
		if language == "en-US" {
			v.Overview = "Summary"
		}
		return v, nil
	}})
	v, err := service.Episode(context.Background(), 12, 0, 1, "ja-JP")
	if err != nil || !reflect.DeepEqual(calls, []string{"ja-JP", "en-US"}) || v.Title != "ja-JP" || v.OverviewSource.RequestedLanguage != "en-US" {
		t.Fatalf("episode=%+v error=%v", v, err)
	}
}

func TestSearchFallbackAdvancesOnlyEmptyPages(t *testing.T) {
	var calls []string
	input := domain.MetadataSearchInput{Query: "Title", Year: 2024, Language: "zh-TW"}
	values, language, err := metadataSearchFallback(context.Background(), input, func(_ context.Context, v domain.MetadataSearchInput) ([]int, error) {
		calls = append(calls, v.Language)
		if v.Query != input.Query || v.Year != input.Year {
			t.Fatal("query changed")
		}
		if v.Language == "ja-JP" {
			return []int{12}, nil
		}
		return []int{}, nil
	})
	if err != nil || language != "ja-JP" || !reflect.DeepEqual(values, []int{12}) || !reflect.DeepEqual(calls, []string{"zh-TW", "ja-JP"}) || input.Language != "zh-TW" {
		t.Fatalf("calls=%v language=%s error=%v", calls, language, err)
	}
	for _, failure := range []error{context.Canceled, context.DeadlineExceeded, domain.ErrMetadataUnavailable} {
		calls = nil
		_, _, err := metadataSearchFallback(context.Background(), input, func(_ context.Context, v domain.MetadataSearchInput) ([]int, error) {
			calls = append(calls, v.Language)
			return nil, failure
		})
		if !errors.Is(err, failure) || len(calls) != 1 {
			t.Fatal("search error continued fallback")
		}
	}
}

func TestSeriesFallbackAndSearchProvenance(t *testing.T) {
	var calls []string
	service, _ := NewMetadata(testSeriesProvider{detail: func(_ context.Context, id int32, language string) (domain.SeriesCandidate, error) {
		calls = append(calls, language)
		v := domain.SeriesCandidate{ProviderID: id, Title: language, Language: language, FetchedAt: time.Now()}
		if language == "en-US" {
			v.Overview = "Summary"
		}
		return v, nil
	}, search: func(_ context.Context, input domain.SeriesSearchInput) ([]domain.SeriesCandidate, error) {
		if input.Language != "ja-JP" {
			return []domain.SeriesCandidate{}, nil
		}
		return []domain.SeriesCandidate{{ProviderID: 12, Title: input.Query, Language: input.Language, Overview: "Summary", FetchedAt: time.Now()}}, nil
	}})
	v, err := service.Series(context.Background(), 12, "ja-JP")
	if err != nil || !reflect.DeepEqual(calls, []string{"ja-JP", "en-US"}) || v.Title != "ja-JP" || v.OverviewSource.RequestedLanguage != "en-US" {
		t.Fatalf("series=%+v error=%v", v, err)
	}
	result, err := service.SearchSeries(context.Background(), domain.SeriesSearchInput{Query: "Title", Language: "zh-TW"})
	if err != nil || len(result.Candidates) != 1 || result.Language != "zh-TW" || result.Candidates[0].Series.Language != "ja-JP" || result.Candidates[0].Series.OverviewSource.RequestedLanguage != "ja-JP" || !result.Candidates[0].NeedsConfirmation {
		t.Fatalf("search=%+v error=%v", result, err)
	}
}
