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

const searchBody = `{"page":1,"results":[{"id":12,"title":"Movie & api_key=attacker","original_title":"Original","release_date":"2024-02-29"}]}`

func TestMovieSearchProviderQueryContract(t *testing.T) {
	c := testMovieClient(t)
	c.fetch = func(ctx context.Context, raw string, limit int64) (outbound.Response, error) {
		u, err := url.Parse(raw)
		if err != nil || u.Path != "/3/search/movie" || u.Host != "api.themoviedb.org" || u.Scheme != "https" || limit != 1<<20 {
			t.Fatal("search endpoint differs")
		}
		query := u.Query()
		if len(query) != 6 || len(query["api_key"]) != 1 || query.Get("api_key") != testKey || query.Get("query") != "Movie & api_key=attacker" || query.Get("primary_release_year") != "2024" || query.Get("language") != "zh-TW" || query.Get("page") != "1" || query.Get("include_adult") != "false" {
			t.Fatal("unescaped/incorrect search query")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("missing search deadline")
		}
		return outbound.Response{Status: 200, Body: []byte(searchBody)}, nil
	}
	result, err := c.SearchMovies(context.Background(), domain.MovieSearchInput{Query: "  Movie & api_key=attacker  ", Year: 2024, Language: "zh-TW"})
	if err != nil || len(result) != 1 || result[0].ProviderID != 12 || result[0].Language != "zh-TW" || result[0].FetchedAt.IsZero() {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestMovieCancelledBeforeCachePublication(t *testing.T) {
	c := testMovieClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := time.Now().UTC()
	clockCalls := 0
	c.now = func() time.Time {
		clockCalls++
		if clockCalls == 2 {
			cancel()
		}
		return now
	}
	c.fetch = func(context.Context, string, int64) (outbound.Response, error) {
		return outbound.Response{Status: 200, Body: []byte(movieBody)}, nil
	}
	if _, err := c.Movie(ctx, 12, "en-US"); err != context.Canceled {
		t.Fatalf("error=%v", err)
	}
	if _, ok := c.movies.get(movieKey{12, "en-US"}, now); ok {
		t.Fatal("cancelled candidate cached")
	}
}

func TestMovieSearchResponseBounds(t *testing.T) {
	var many []string
	for i := 1; i <= 21; i++ {
		many = append(many, fmt.Sprintf(`{"id":%d,"title":"Movie"}`, i))
	}
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
		count  int
	}{
		{"valid", 200, searchBody, nil, 1}, {"empty", 200, `{"page":1,"results":[]}`, nil, 0},
		{"missing", 200, `{"page":1}`, ErrResponse, 0}, {"null", 200, `{"page":1,"results":null}`, ErrResponse, 0},
		{"page", 200, `{"page":2,"results":[]}`, ErrResponse, 0}, {"malformed", 200, "secret", ErrResponse, 0},
		{"too_many", 200, `{"page":1,"results":[` + strings.Join(many, ",") + `]}`, ErrResponse, 0},
		{"duplicate_id", 200, `{"page":1,"results":[{"id":12,"title":"a"},{"id":12,"title":"b"}]}`, ErrResponse, 0},
		{"invalid_id", 200, `{"page":1,"results":[{"id":0,"title":"a"}]}`, ErrResponse, 0},
		{"overflow_id", 200, `{"page":1,"results":[{"id":2147483648,"title":"a"}]}`, ErrResponse, 0},
		{"blank_title", 200, `{"page":1,"results":[{"id":12,"title":" "}]}`, ErrResponse, 0},
		{"bad_date", 200, `{"page":1,"results":[{"id":12,"title":"a","release_date":"2024-02-30"}]}`, ErrResponse, 0},
		{"secret", 200, `{"page":1,"results":[{"id":12,"title":"` + testKey + `"}]}`, ErrResponse, 0},
		{"credentials", 401, "secret", ErrCredentials, 0}, {"rate", 429, "", ErrRateLimited, 0}, {"server", 500, "", ErrUnavailable, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testMovieClient(t)
			c.fetch = func(context.Context, string, int64) (outbound.Response, error) {
				return outbound.Response{Status: tc.status, Body: []byte(tc.body)}, nil
			}
			result, err := c.SearchMovies(context.Background(), domain.MovieSearchInput{Query: "Movie", Language: "en-US"})
			if !errors.Is(err, tc.want) || len(result) != tc.count || (err != nil && result != nil) {
				t.Fatalf("count=%d error=%v", len(result), err)
			}
		})
	}
}

func TestMovieSearchInputAndCancellation(t *testing.T) {
	c := testMovieClient(t)
	c.fetch = func(context.Context, string, int64) (outbound.Response, error) {
		t.Fatal("invalid query reached provider")
		return outbound.Response{}, nil
	}
	for _, input := range []domain.MovieSearchInput{{Query: "", Language: "en-US"}, {Query: "\n", Language: "en-US"}, {Query: "a\nsecret", Language: "en-US"}, {Query: strings.Repeat("x", 257), Language: "en-US"}, {Query: "\xff", Language: "en-US"}, {Query: "Movie", Year: 999, Language: "en-US"}, {Query: "Movie", Year: 10000, Language: "en-US"}, {Query: "Movie", Language: "fr-FR"}, {Query: testKey, Language: "en-US"}} {
		if _, err := c.SearchMovies(context.Background(), input); err != domain.ErrInvalid {
			t.Fatal("invalid input accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.SearchMovies(ctx, domain.MovieSearchInput{Query: "Movie", Language: "en-US"}); err != context.Canceled {
		t.Fatal("cancelled query accepted")
	}
	for _, tc := range []struct{ source, want error }{{errors.New("secret://10.0.0.1"), ErrUnavailable}, {context.Canceled, context.Canceled}, {context.DeadlineExceeded, context.DeadlineExceeded}} {
		c.fetch = func(context.Context, string, int64) (outbound.Response, error) { return outbound.Response{}, tc.source }
		if _, err := c.SearchMovies(context.Background(), domain.MovieSearchInput{Query: "Movie", Language: "en-US"}); !errors.Is(err, tc.want) {
			t.Fatal("unsafe error mapping")
		}
	}
}
