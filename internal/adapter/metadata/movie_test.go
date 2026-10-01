package metadata

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/outbound"
)

const movieBody = `{"id":12,"title":"電影","original_title":"Movie","overview":"Summary","release_date":"2024-02-29","homepage":"http://127.0.0.1/?secret=bad"}`

func testMovieClient(t *testing.T) *TMDB {
	t.Helper()
	c, err := NewTMDB(testKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	c.governor = nil
	c.wait = func(context.Context, time.Duration) error { return nil }
	return c
}

func TestMovieFetchCacheLanguageAndExpiry(t *testing.T) {
	c := testMovieClient(t)
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	calls := 0
	c.fetch = func(ctx context.Context, raw string, limit int64) (outbound.Response, error) {
		calls++
		u, err := url.Parse(raw)
		if err != nil || u.Host != "api.themoviedb.org" || u.Scheme != "https" || u.Path != "/3/movie/12" || u.Query().Get("api_key") != testKey || !domain.ValidMetadataLanguage(u.Query().Get("language")) || limit != 1<<20 {
			t.Fatal("movie request contract differs")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("no total deadline")
		}
		return outbound.Response{Status: 200, Body: []byte(movieBody)}, nil
	}
	one, err := c.Movie(context.Background(), 12, "zh-TW")
	if err != nil || one.Source != "TMDB" || one.SourceURL != "https://www.themoviedb.org/movie/12" || !one.FetchedAt.Equal(now) || one.Language != "zh-TW" {
		t.Fatalf("candidate=%+v error=%v", one, err)
	}
	one.Title = "changed locally"
	now = now.Add(23 * time.Hour)
	two, err := c.Movie(context.Background(), 12, "zh-TW")
	if err != nil || two.Title != "電影" || calls != 1 || !two.FetchedAt.Equal(now.Add(-23*time.Hour)) {
		t.Fatal("cache hit mutates data or extends TTL")
	}
	if _, err := c.Movie(context.Background(), 12, "ja-JP"); err != nil || calls != 2 {
		t.Fatal("language cache collision")
	}
	now = now.Add(time.Hour)
	if _, err := c.Movie(context.Background(), 12, "zh-TW"); err != nil || calls != 3 {
		t.Fatal("expired candidate reused")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Movie(ctx, 12, "ja-JP"); !errors.Is(err, context.Canceled) || calls != 3 {
		t.Fatal("cancelled cache hit accepted")
	}
}

func TestMovieProviderResponseContract(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"ok", 200, movieBody, nil}, {"not_found", 404, "secret", domain.ErrNotFound}, {"credentials", 401, "secret", ErrCredentials}, {"forbidden", 403, "", ErrCredentials}, {"rate", 429, "", ErrRateLimited}, {"server", 503, "", ErrUnavailable},
		{"invalid_json", 200, "secret", ErrResponse}, {"wrong_id", 200, strings.Replace(movieBody, `"id":12`, `"id":13`, 1), ErrResponse},
		{"missing_title", 200, `{"id":12}`, ErrResponse}, {"invalid_date", 200, strings.Replace(movieBody, "2024-02-29", "2023-02-29", 1), ErrResponse},
		{"secret_title", 200, `{"id":12,"title":"` + testKey + `"}`, ErrResponse}, {"control", 200, `{"id":12,"title":"bad\u0000"}`, ErrResponse},
		{"large_title", 200, `{"id":12,"title":"` + strings.Repeat("x", 1025) + `"}`, ErrResponse}, {"large_overview", 200, `{"id":12,"title":"valid","overview":"` + strings.Repeat("x", 16385) + `"}`, ErrResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testMovieClient(t)
			calls := 0
			c.fetch = func(context.Context, string, int64) (outbound.Response, error) {
				calls++
				return outbound.Response{Status: tc.status, Body: []byte(tc.body)}, nil
			}
			_, err := c.Movie(context.Background(), 12, "en-US")
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v", err)
			}
			firstCalls := calls
			_, err = c.Movie(context.Background(), 12, "en-US")
			if !errors.Is(err, tc.want) || (tc.want != nil && calls != firstCalls*2) || (tc.want == nil && calls != firstCalls) {
				t.Fatal("unexpected cache policy")
			}
		})
	}
}

func TestMovieRejectsInputAndSanitizesTransportFailures(t *testing.T) {
	c := testMovieClient(t)
	c.fetch = func(context.Context, string, int64) (outbound.Response, error) {
		t.Fatal("invalid request reached provider")
		return outbound.Response{}, nil
	}
	for _, input := range []movieKey{{0, "en-US"}, {-1, "en-US"}, {12, ""}, {12, "fr-FR"}, {12, "en-US&api_key=secret"}} {
		if _, err := c.Movie(context.Background(), input.id, input.language); err != domain.ErrInvalid {
			t.Fatal("invalid input accepted")
		}
	}
	for _, tc := range []struct{ source, want error }{{errors.New("https://secret@10.0.0.1/?api_key=" + testKey), ErrUnavailable}, {context.Canceled, context.Canceled}, {context.DeadlineExceeded, context.DeadlineExceeded}} {
		c.fetch = func(context.Context, string, int64) (outbound.Response, error) { return outbound.Response{}, tc.source }
		if _, err := c.Movie(context.Background(), 12, "en-US"); !errors.Is(err, tc.want) {
			t.Fatalf("unsafe error=%v", err)
		}
	}
}

func TestMovieCacheLRUBoundsAndNewerResult(t *testing.T) {
	var c movieCache
	now := time.Now().UTC()
	for id := int32(1); id <= movieCacheCapacity; id++ {
		c.put(movieKey{id, "en-US"}, domain.MovieCandidate{ProviderID: id, FetchedAt: now})
	}
	if _, ok := c.get(movieKey{1, "en-US"}, now); !ok {
		t.Fatal("missing cache entry")
	}
	c.put(movieKey{257, "en-US"}, domain.MovieCandidate{ProviderID: 257, FetchedAt: now})
	if len(c.entries) != 256 || c.lru.Len() != 256 {
		t.Fatal("cache exceeded capacity")
	}
	if _, ok := c.get(movieKey{2, "en-US"}, now); ok {
		t.Fatal("LRU victim retained")
	}
	c.put(movieKey{1, "en-US"}, domain.MovieCandidate{Title: "new", FetchedAt: now.Add(time.Second)})
	c.put(movieKey{1, "en-US"}, domain.MovieCandidate{Title: "old", FetchedAt: now})
	if value, ok := c.get(movieKey{1, "en-US"}, now); !ok || value.Title != "new" {
		t.Fatal("older concurrent result replaced newer data")
	}
}

func TestMovieCacheConcurrentAccess(t *testing.T) {
	var c movieCache
	var wg sync.WaitGroup
	var hits atomic.Int32
	now := time.Now().UTC()
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			for id := int32(1); id < 1024; id++ {
				key := movieKey{id, "en-US"}
				c.put(key, domain.MovieCandidate{ProviderID: id, FetchedAt: now})
				if _, ok := c.get(key, now); ok {
					hits.Add(1)
				}
			}
		})
	}
	wg.Wait()
	if hits.Load() == 0 || len(c.entries) > 256 || c.lru.Len() != len(c.entries) {
		t.Fatal("cache bounded concurrency invariant failed")
	}
}
