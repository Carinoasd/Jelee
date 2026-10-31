package metadata

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/outbound"
)

const (
	seasonImageBody  = `{"id":3624,"posters":[{"iso_639_1":"ja","file_path":"/season.jpg","width":500,"height":750,"vote_average":5,"vote_count":1}]}`
	episodeImageBody = `{"id":63056,"stills":[{"iso_639_1":null,"file_path":"/still.jpg","width":1280,"height":720,"vote_average":6,"vote_count":2}]}`
)

func TestEpisodeImagesEndpointsMappingAndCachePartitions(t *testing.T) {
	c := testMovieClient(t)
	calls := map[string]int{}
	c.fetch = func(ctx context.Context, raw string, limit int64) (outbound.Response, error) {
		u, _ := url.Parse(raw)
		if u.Host != "api.themoviedb.org" || u.Scheme != "https" || u.Query().Get("api_key") != testKey || u.Query().Get("include_image_language") != "ja,null" || u.Query().Get("language") != "" || limit != 1<<20 {
			t.Fatal("episode image query differs")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded episode image query")
		}
		calls[u.Path]++
		if strings.Contains(u.Path, "/episode/") {
			return outbound.Response{Status: 200, Body: []byte(episodeImageBody)}, nil
		}
		return outbound.Response{Status: 200, Body: []byte(seasonImageBody)}, nil
	}
	languages := []string{"ja", "null"}
	season, err := c.EpisodeImages(context.Background(), 1399, 0, 0, languages)
	if err != nil || season.Resource != "season" || season.ProviderID != 3624 || season.SeriesID != 1399 || season.SeasonNumber == nil || *season.SeasonNumber != 0 || season.EpisodeNumber != 0 || season.SourceURL != "https://www.themoviedb.org/tv/1399/season/0" || len(season.Candidates) != 1 || season.Candidates[0].Kind != "poster" {
		t.Fatalf("season=%+v error=%v", season, err)
	}
	episode, err := c.EpisodeImages(context.Background(), 1399, 1, 2, languages)
	if err != nil || episode.Resource != "episode" || episode.ProviderID != 63056 || *episode.SeasonNumber != 1 || episode.EpisodeNumber != 2 || episode.SourceURL != "https://www.themoviedb.org/tv/1399/season/1/episode/2" || len(episode.Candidates) != 1 || episode.Candidates[0].Kind != "still" || episode.Candidates[0].Language != "null" || episode.Candidates[0].URL != "https://image.tmdb.org/t/p/original/still.jpg" {
		t.Fatalf("episode=%+v error=%v", episode, err)
	}
	// Returned values own their season number and slices.
	*season.SeasonNumber = 9
	season.Candidates[0].FilePath = "changed"
	again, err := c.EpisodeImages(context.Background(), 1399, 0, 0, languages)
	if err != nil || *again.SeasonNumber != 0 || again.Candidates[0].FilePath != "/season.jpg" || calls["/3/tv/1399/season/0/images"] != 1 {
		t.Fatal("season image cache aliased or missed")
	}
	// Season, episode, other season and the series itself never share a key.
	if _, err := c.EpisodeImages(context.Background(), 1399, 1, 0, languages); err != nil || calls["/3/tv/1399/season/1/images"] != 1 {
		t.Fatal("season partition collision")
	}
	if _, err := c.EpisodeImages(context.Background(), 1399, 1, 3, languages); err != nil || calls["/3/tv/1399/season/1/episode/3/images"] != 1 {
		t.Fatal("episode partition collision")
	}
}

func TestEpisodeImagesRejectInvalidTargetsAndResponses(t *testing.T) {
	c := testMovieClient(t)
	calls := 0
	c.fetch = func(context.Context, string, int64) (outbound.Response, error) {
		calls++
		return outbound.Response{Status: 200, Body: []byte(seasonImageBody)}, nil
	}
	for _, target := range [][3]int32{{0, 1, 0}, {-1, 1, 1}, {1, -1, 0}, {1, 1, -1}} {
		if _, err := c.EpisodeImages(context.Background(), target[0], target[1], target[2], []string{"en"}); err != domain.ErrInvalid {
			t.Fatalf("invalid target %v accepted", target)
		}
	}
	if _, err := c.EpisodeImages(context.Background(), 1, 1, 0, []string{"en-US"}); err != domain.ErrInvalid || calls != 0 {
		t.Fatal("invalid language preference reached the provider")
	}
	for _, tc := range []struct {
		name, body string
		episode    int32
		status     int
		want       error
	}{
		{"zero_id", strings.Replace(seasonImageBody, `"id":3624`, `"id":0`, 1), 0, 200, ErrResponse},
		{"missing_id", strings.Replace(seasonImageBody, `"id":3624,`, "", 1), 0, 200, ErrResponse},
		{"season_without_posters", `{"id":3624}`, 0, 200, ErrResponse},
		{"episode_without_stills", strings.Replace(episodeImageBody, "stills", "posters", 1), 2, 200, ErrResponse},
		{"null_stills", `{"id":63056,"stills":null}`, 2, 200, ErrResponse},
		{"bad_still_path", strings.Replace(episodeImageBody, "/still.jpg", "/../still.jpg", 1), 2, 200, ErrResponse},
		{"not_found", "secret", 2, 404, domain.ErrNotFound},
		{"throttled", "", 2, 429, ErrRateLimited},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testMovieClient(t)
			calls := 0
			c.fetch = func(context.Context, string, int64) (outbound.Response, error) {
				calls++
				return outbound.Response{Status: tc.status, Body: []byte(tc.body)}, nil
			}
			for i := 0; i < 2; i++ {
				if _, err := c.EpisodeImages(context.Background(), 1399, 1, tc.episode, []string{"null"}); !errors.Is(err, tc.want) {
					t.Fatalf("error=%v", err)
				}
			}
			wantCalls := 2
			if tc.status == 429 {
				wantCalls = 6
			}
			if calls != wantCalls {
				t.Fatalf("invalid response cached calls=%d", calls)
			}
		})
	}
	// Season posters ignore unrelated arrays such as backdrops or stills.
	c = testMovieClient(t)
	c.fetch = func(context.Context, string, int64) (outbound.Response, error) {
		return outbound.Response{Status: 200, Body: []byte(`{"id":3624,"posters":[],"stills":[{"iso_639_1":null,"file_path":"/x.jpg","width":1,"height":1,"vote_average":1,"vote_count":1}]}`)}, nil
	}
	if value, err := c.EpisodeImages(context.Background(), 1399, 1, 0, []string{"null"}); err != nil || len(value.Candidates) != 0 {
		t.Fatalf("season mixed in stills: %+v %v", value, err)
	}
}
