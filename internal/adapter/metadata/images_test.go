package metadata

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/outbound"
)

const imageBody = `{"id":12,"posters":[{"iso_639_1":null,"file_path":"/poster.jpg","width":500,"height":750,"vote_average":8,"vote_count":10}],"backdrops":[]}`

func TestImageCandidatesCachePreferenceKeyAndOwnership(t *testing.T) {
	c := testMovieClient(t)
	if c.images.capacity != 16 {
		t.Fatal("unbounded image cache")
	}
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	calls := 0
	c.fetch = func(ctx context.Context, raw string, limit int64) (outbound.Response, error) {
		calls++
		u, _ := url.Parse(raw)
		if u.Host != "api.themoviedb.org" || u.Scheme != "https" || !strings.HasSuffix(u.Path, "/12/images") || u.Query().Get("api_key") != testKey || u.Query().Get("language") != "" || u.Query().Get("include_image_language") == "" || limit != 1<<20 {
			t.Fatal("image query differs")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded image query")
		}
		return outbound.Response{Status: 200, Body: []byte(imageBody)}, nil
	}
	languages := []string{"en", "null"}
	v, err := c.Images(context.Background(), "movie", 12, languages)
	if err != nil || v.SourceURL != "https://www.themoviedb.org/movie/12" || len(v.Candidates) != 1 || v.Candidates[0].URL != "https://image.tmdb.org/t/p/original/poster.jpg" || v.Candidates[0].Language != "null" {
		t.Fatalf("images=%+v error=%v", v, err)
	}
	languages[0] = "zh"
	v.ImageLanguages[0] = "ja"
	v.Candidates[0].FilePath = "changed"
	second, err := c.Images(context.Background(), "movie", 12, []string{"en", "null"})
	if err != nil || second.Candidates[0].FilePath != "/poster.jpg" || !reflect.DeepEqual(second.ImageLanguages, []string{"en", "null"}) || calls != 1 {
		t.Fatal("insert alias mutated cache")
	}
	second.Candidates[0].FilePath = "changed hit"
	third, _ := c.Images(context.Background(), "movie", 12, []string{"en", "null"})
	if third.Candidates[0].FilePath != "/poster.jpg" {
		t.Fatal("cache hit alias")
	}
	if _, err = c.Images(context.Background(), "movie", 12, []string{"null", "en"}); err != nil || calls != 2 {
		t.Fatal("ordered preference cache collision")
	}
	if _, err = c.Images(context.Background(), "series", 12, []string{"en", "null"}); err != nil || calls != 3 {
		t.Fatal("resource cache collision")
	}
	now = now.Add(24 * time.Hour)
	if _, err = c.Images(context.Background(), "movie", 12, []string{"en", "null"}); err != nil || calls != 4 {
		t.Fatal("expired images reused")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = c.Images(ctx, "movie", 12, []string{"en", "null"}); err != context.Canceled || calls != 4 {
		t.Fatal("cancelled cache hit accepted")
	}
}

func TestImageCandidatesRejectMalformedResponsesAndDoNotCacheErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       error
	}{
		{"wrong_id", strings.Replace(imageBody, `"id":12`, `"id":13`, 1), 200, ErrResponse},
		{"missing_posters", `{"id":12,"backdrops":[]}`, 200, ErrResponse},
		{"null_list", `{"id":12,"posters":null,"backdrops":[]}`, 200, ErrResponse},
		{"missing_language", strings.Replace(imageBody, `"iso_639_1":null,`, "", 1), 200, ErrResponse},
		{"string_null", strings.Replace(imageBody, `"iso_639_1":null`, `"iso_639_1":"null"`, 1), 200, ErrResponse},
		{"region_language", strings.Replace(imageBody, `"iso_639_1":null`, `"iso_639_1":"zh-TW"`, 1), 200, ErrResponse},
		{"zero_width", strings.Replace(imageBody, `"width":500`, `"width":0`, 1), 200, ErrResponse},
		{"too_large", strings.Replace(imageBody, `"height":750`, `"height":32769`, 1), 200, ErrResponse},
		{"missing_votes", strings.Replace(imageBody, `,"vote_count":10`, "", 1), 200, ErrResponse},
		{"bad_score", strings.Replace(imageBody, `"vote_average":8`, `"vote_average":11`, 1), 200, ErrResponse},
		{"not_found", "secret", 404, domain.ErrNotFound}, {"auth", "secret", 401, ErrCredentials}, {"invalid_json", "secret", 200, ErrResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testMovieClient(t)
			calls := 0
			c.fetch = func(context.Context, string, int64) (outbound.Response, error) {
				calls++
				return outbound.Response{Status: tc.status, Body: []byte(tc.body)}, nil
			}
			for i := 0; i < 2; i++ {
				if _, err := c.Images(context.Background(), "movie", 12, []string{"en", "null"}); !errors.Is(err, tc.want) {
					t.Fatalf("error=%v", err)
				}
			}
			if calls != 2 {
				t.Fatal("invalid response cached")
			}
		})
	}
	for _, path := range []string{"http://127.0.0.1/x.jpg", "//host/x.jpg", "/../x.jpg", "/a%2fb.jpg", "/a.jpg?key=secret", "/a.svg", "/a.jpg#fragment", "/a\n.jpg", "/" + testKey + ".jpg"} {
		t.Run(path, func(t *testing.T) {
			c := testMovieClient(t)
			body := strings.Replace(imageBody, `"/poster.jpg"`, fmt.Sprintf("%q", path), 1)
			c.fetch = func(context.Context, string, int64) (outbound.Response, error) {
				return outbound.Response{Status: 200, Body: []byte(body)}, nil
			}
			if _, err := c.Images(context.Background(), "movie", 12, []string{"null"}); err != ErrResponse {
				t.Fatal("unsafe path accepted")
			}
		})
	}
	for _, count := range []int{2, domain.MetadataImageLimit, domain.MetadataImageLimit + 1} {
		c := testMovieClient(t)
		one := `{"iso_639_1":null,"file_path":"/p.jpg","width":500,"height":750,"vote_average":8,"vote_count":10}`
		body := `{"id":12,"posters":[` + strings.Repeat(one+",", count-1) + one + `],"backdrops":[]}`
		if count >= domain.MetadataImageLimit {
			entries := make([]string, count)
			for i := range entries {
				entries[i] = strings.Replace(one, "/p.jpg", fmt.Sprintf("/p%04d.jpg", i), 1)
			}
			body = `{"id":12,"posters":[` + strings.Join(entries, ",") + `],"backdrops":[]}`
		}
		c.fetch = func(context.Context, string, int64) (outbound.Response, error) {
			return outbound.Response{Status: 200, Body: []byte(body)}, nil
		}
		value, err := c.Images(context.Background(), "movie", 12, []string{"null"})
		if count == domain.MetadataImageLimit {
			if err != nil || len(value.Candidates) != domain.MetadataImageLimit {
				t.Fatal("valid boundary rejected", err)
			}
			continue
		}
		if err != ErrResponse {
			t.Fatal("duplicate or excessive image list accepted")
		}
	}
}

func TestImageCancellationBeforePublicationDoesNotCache(t *testing.T) {
	c := testMovieClient(t)
	calls, clockCalls := 0, 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.now = func() time.Time {
		clockCalls++
		if clockCalls == 2 {
			cancel()
		}
		return time.Now()
	}
	c.fetch = func(context.Context, string, int64) (outbound.Response, error) {
		calls++
		return outbound.Response{Status: 200, Body: []byte(imageBody)}, nil
	}
	if _, err := c.Images(ctx, "movie", 12, []string{"null"}); err != context.Canceled {
		t.Fatal("cancelled parse published", err)
	}
	c.now = time.Now
	if _, err := c.Images(context.Background(), "movie", 12, []string{"null"}); err != nil || calls != 2 {
		t.Fatal("cancelled candidate cached", err)
	}
}
