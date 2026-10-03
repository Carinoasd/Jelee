package metadata

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/outbound"
)

const episodeBody = `{"id":900,"season_number":0,"episode_number":1,"name":"Special","air_date":"2024-01-01"}`
const seasonBody = `{"id":500,"season_number":0,"name":"Specials","episodes":[` + episodeBody + `]}`

func TestSeasonEpisodeCacheKeysAndCloneIsolation(t *testing.T) {
	c := testMovieClient(t)
	now := time.Now().UTC()
	c.now = func() time.Time { return now }
	calls := 0
	c.fetch = func(ctx context.Context, raw string, limit int64) (outbound.Response, error) {
		calls++
		u, _ := url.Parse(raw)
		if limit != 1<<20 || u.Query().Get("api_key") != testKey {
			t.Fatal("season request differs")
		}
		body := seasonBody
		if strings.Contains(u.Path, "/episode/") {
			body = episodeBody
		}
		return outbound.Response{Status: 200, Body: []byte(body)}, nil
	}
	season, err := c.Season(context.Background(), 12, 0, "zh-TW")
	if err != nil || len(season.Episodes) != 1 || season.SourceURL != "https://www.themoviedb.org/tv/12/season/0" || season.Episodes[0].SourceURL != "https://www.themoviedb.org/tv/12/season/0/episode/1" {
		t.Fatalf("season=%+v error=%v", season, err)
	}
	season.Episodes[0].Title = "mutated first return"
	second, err := c.Season(context.Background(), 12, 0, "zh-TW")
	if err != nil || second.Episodes[0].Title != "Special" || calls != 1 {
		t.Fatal("cache insertion aliased episodes")
	}
	second.Episodes[0].Title = "mutated cache hit"
	third, err := c.Season(context.Background(), 12, 0, "zh-TW")
	if err != nil || third.Episodes[0].Title != "Special" || calls != 1 {
		t.Fatal("cache hit aliased episodes")
	}
	for i := 0; i < 2; i++ {
		episode, err := c.Episode(context.Background(), 12, 0, 1, "zh-TW")
		if err != nil || episode.ProviderID != 900 || episode.SeriesID != 12 || episode.SeasonNumber != 0 || episode.EpisodeNumber != 1 {
			t.Fatal("episode differs")
		}
	}
	if calls != 2 {
		t.Fatal("season/episode cache collision or no episode cache")
	}
	if _, err := c.Episode(context.Background(), 13, 0, 1, "zh-TW"); err != nil || calls != 3 {
		t.Fatal("parent series key collision")
	}
	if _, err := c.Episode(context.Background(), 12, 0, 1, "ja-JP"); err != nil || calls != 4 {
		t.Fatal("language key collision")
	}
	now = now.Add(24 * time.Hour)
	if _, err := c.Season(context.Background(), 12, 0, "zh-TW"); err != nil || calls != 5 {
		t.Fatal("season expired data reused")
	}
	if _, err := c.Episode(context.Background(), 12, 0, 1, "zh-TW"); err != nil || calls != 6 {
		t.Fatal("episode expired data reused")
	}
	if c.seasons.capacity != 16 {
		t.Fatal("large season cache capacity differs")
	}
	for id := int32(1); id <= 17; id++ {
		c.seasons.put(episodeKey{id, 1, 0, "en-US"}, domain.SeasonCandidate{FetchedAt: now})
	}
	if len(c.seasons.entries) != 16 {
		t.Fatal("season cache exceeds capacity")
	}
}

func TestSeasonRejectsNumberMismatchAndIncompleteLists(t *testing.T) {
	var many []string
	for i := 1; i <= 1001; i++ {
		many = append(many, fmt.Sprintf(`{"id":%d,"season_number":0,"episode_number":%d,"name":"Episode"}`, i, i))
	}
	for _, body := range []string{
		strings.Replace(seasonBody, `"season_number":0`, `"season_number":1`, 1),
		strings.Replace(seasonBody, `"season_number":0,`, "", 1),
		strings.Replace(seasonBody, `"name":"Specials"`, `"show_id":13,"name":"Specials"`, 1),
		`{"id":500,"season_number":0,"name":"Specials"}`, `{"id":500,"season_number":0,"name":"Specials","episodes":null}`,
		`{"id":500,"season_number":0,"name":"Specials","episodes":[` + episodeBody + `,` + episodeBody + `]}`,
		`{"id":500,"season_number":0,"name":"Specials","episodes":[` + episodeBody + `,` + strings.Replace(episodeBody, `"id":900`, `"id":901`, 1) + `]}`,
		`{"id":500,"season_number":0,"name":"Specials","episodes":[` + episodeBody + `,` + strings.Replace(episodeBody, `"episode_number":1`, `"episode_number":2`, 1) + `]}`,
		`{"id":500,"season_number":0,"name":"Specials","episodes":[` + strings.Replace(episodeBody, `"season_number":0`, `"season_number":1`, 1) + `]}`,
		`{"id":500,"season_number":0,"name":"Specials","episodes":[` + strings.Replace(episodeBody, `"episode_number":1`, `"episode_number":0`, 1) + `]}`,
		`{"id":500,"season_number":0,"name":"Specials","episodes":[` + strings.Join(many, ",") + `]}`,
	} {
		c := testMovieClient(t)
		c.fetch = func(context.Context, string, int64) (outbound.Response, error) {
			return outbound.Response{Status: 200, Body: []byte(body)}, nil
		}
		if _, err := c.Season(context.Background(), 12, 0, "en-US"); err != ErrResponse {
			t.Fatal("invalid season accepted")
		}
		if len(c.seasons.entries) != 0 {
			t.Fatal("invalid season cached")
		}
	}
}

func TestEpisodeIdentityValidationErrorsAndCancellation(t *testing.T) {
	for _, body := range []string{strings.Replace(episodeBody, `"episode_number":1`, `"episode_number":2`, 1), strings.Replace(episodeBody, `"season_number":0`, `"season_number":1`, 1), strings.Replace(episodeBody, `"season_number":0,`, "", 1), strings.Replace(episodeBody, `"name":"Special"`, `"show_id":13,"name":"Special"`, 1), strings.Replace(episodeBody, "2024-01-01", "2024-02-30", 1), `{"id":900,"season_number":0,"episode_number":1,"name":"` + testKey + `"}`} {
		c := testMovieClient(t)
		c.fetch = func(context.Context, string, int64) (outbound.Response, error) {
			return outbound.Response{Status: 200, Body: []byte(body)}, nil
		}
		if _, err := c.Episode(context.Background(), 12, 0, 1, "en-US"); err != ErrResponse {
			t.Fatal("invalid episode accepted")
		}
	}
	for _, tc := range []struct {
		status int
		want   error
	}{{404, domain.ErrNotFound}, {401, ErrCredentials}, {429, ErrRateLimited}, {500, ErrUnavailable}} {
		c := testMovieClient(t)
		calls := 0
		c.fetch = func(context.Context, string, int64) (outbound.Response, error) {
			calls++
			return outbound.Response{Status: tc.status, Body: []byte("secret")}, nil
		}
		for i := 0; i < 2; i++ {
			if _, err := c.Episode(context.Background(), 12, 0, 1, "en-US"); !errors.Is(err, tc.want) {
				t.Fatal("unsafe error")
			}
		}
		if calls < 2 {
			t.Fatal("error cached")
		}
	}
	c := testMovieClient(t)
	c.fetch = func(context.Context, string, int64) (outbound.Response, error) {
		t.Fatal("invalid input reached provider")
		return outbound.Response{}, nil
	}
	if _, err := c.Season(context.Background(), 12, -1, "en-US"); err != domain.ErrInvalid {
		t.Fatal("negative season accepted")
	}
	if _, err := c.Episode(context.Background(), 12, 0, 0, "en-US"); err != domain.ErrInvalid {
		t.Fatal("zero episode accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Season(ctx, 12, 0, "en-US"); err != context.Canceled {
		t.Fatal("cancelled season accepted")
	}
	if _, err := c.Episode(ctx, 12, 0, 1, "en-US"); err != context.Canceled {
		t.Fatal("cancelled episode accepted")
	}
}
