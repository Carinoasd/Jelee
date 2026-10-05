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

const findMovieBody = `{"movie_results":[{"id":12,"title":"電影","original_title":"Movie","release_date":"2024-02-29"}],"tv_results":[],"person_results":[{"id":3}]}`

// waitFlightWaiters blocks until key's flight has n registered waiters.
func waitFlightWaiters[K comparable, V any](t *testing.T, g *flightGroup[K, V], key K, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		g.mu.Lock()
		f := g.flights[key]
		count := 0
		if f != nil {
			count = len(f.waiters)
		}
		g.mu.Unlock()
		if count == n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("flight never reached %d waiters", n)
}

func flightLen[K comparable, V any](g *flightGroup[K, V]) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.flights)
}

// blockingFetch serves body for every request after release is closed and
// counts provider requests per path.
func blockingFetch(t *testing.T, c *TMDB, release <-chan struct{}) *sync.Map {
	t.Helper()
	counts := new(sync.Map)
	c.fetch = func(ctx context.Context, raw string, _ int64) (outbound.Response, error) {
		u, err := url.Parse(raw)
		if err != nil {
			t.Error("unparsable request")
			return outbound.Response{}, err
		}
		n, _ := counts.LoadOrStore(u.Path+"?"+u.Query().Get("language"), new(atomic.Int32))
		n.(*atomic.Int32).Add(1)
		select {
		case <-release:
		case <-ctx.Done():
			return outbound.Response{}, ctx.Err()
		}
		body := ""
		switch {
		case strings.HasPrefix(u.Path, "/3/movie/"):
			body = movieBody
		case strings.Contains(u.Path, "/episode/"):
			body = episodeBody
		case strings.Contains(u.Path, "/season/"):
			body = seasonBody
		case strings.HasPrefix(u.Path, "/3/tv/"):
			body = seriesBody
		case strings.HasPrefix(u.Path, "/3/find/"):
			body = findMovieBody
		}
		return outbound.Response{Status: 200, Body: []byte(body)}, nil
	}
	return counts
}

func requestCount(counts *sync.Map, key string) int32 {
	n, ok := counts.Load(key)
	if !ok {
		return 0
	}
	return n.(*atomic.Int32).Load()
}

func TestConcurrentMissesShareOneProviderRequest(t *testing.T) {
	for _, tc := range []struct {
		name    string
		path    string
		lookup  func(*TMDB, context.Context) (any, error)
		waiters func(*testing.T, *TMDB, int)
	}{
		{"movie", "/3/movie/12?zh-TW", func(c *TMDB, ctx context.Context) (any, error) { return c.Movie(ctx, 12, "zh-TW") },
			func(t *testing.T, c *TMDB, n int) {
				waitFlightWaiters(t, &c.movieFlights, candidateKey{12, "zh-TW"}, n)
			}},
		{"series", "/3/tv/12?zh-TW", func(c *TMDB, ctx context.Context) (any, error) { return c.Series(ctx, 12, "zh-TW") },
			func(t *testing.T, c *TMDB, n int) {
				waitFlightWaiters(t, &c.seriesFlights, candidateKey{12, "zh-TW"}, n)
			}},
		{"season", "/3/tv/12/season/0?zh-TW", func(c *TMDB, ctx context.Context) (any, error) { return c.Season(ctx, 12, 0, "zh-TW") },
			func(t *testing.T, c *TMDB, n int) {
				waitFlightWaiters(t, &c.seasonFlights, episodeKey{12, 0, 0, "zh-TW"}, n)
			}},
		{"episode", "/3/tv/12/season/0/episode/1?zh-TW", func(c *TMDB, ctx context.Context) (any, error) { return c.Episode(ctx, 12, 0, 1, "zh-TW") },
			func(t *testing.T, c *TMDB, n int) {
				waitFlightWaiters(t, &c.episodeFlights, episodeKey{12, 0, 1, "zh-TW"}, n)
			}},
		{"find", "/3/find/tt0137523?zh-TW", func(c *TMDB, ctx context.Context) (any, error) {
			return c.FindByExternalID(ctx, domain.ExternalSourceIMDb, "tt0137523", "zh-TW")
		}, func(t *testing.T, c *TMDB, n int) {
			waitFlightWaiters(t, &c.externalFlights, externalKey{domain.ExternalSourceIMDb, "tt0137523", "zh-TW"}, n)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testMovieClient(t)
			release := make(chan struct{})
			counts := blockingFetch(t, c, release)
			const callers = 8
			results := make(chan error, callers)
			var wg sync.WaitGroup
			for i := 0; i < callers; i++ {
				wg.Go(func() {
					_, err := tc.lookup(c, context.Background())
					results <- err
				})
			}
			tc.waiters(t, c, callers)
			close(release)
			wg.Wait()
			close(results)
			for err := range results {
				if err != nil {
					t.Fatalf("coalesced waiter error=%v", err)
				}
			}
			if got := requestCount(counts, tc.path); got != 1 {
				t.Fatalf("provider requests=%d", got)
			}
			if _, err := tc.lookup(c, context.Background()); err != nil || requestCount(counts, tc.path) != 1 {
				t.Fatal("coalesced result was not cached")
			}
		})
	}
}

func TestConcurrentMissKeysStayIsolated(t *testing.T) {
	c := testMovieClient(t)
	release := make(chan struct{})
	counts := blockingFetch(t, c, release)
	var wg sync.WaitGroup
	for _, language := range []string{"zh-TW", "ja-JP", "zh-TW", "ja-JP"} {
		wg.Go(func() {
			if _, err := c.Movie(context.Background(), 12, language); err != nil {
				t.Error(err)
			}
		})
	}
	waitFlightWaiters(t, &c.movieFlights, candidateKey{12, "zh-TW"}, 2)
	waitFlightWaiters(t, &c.movieFlights, candidateKey{12, "ja-JP"}, 2)
	close(release)
	wg.Wait()
	if requestCount(counts, "/3/movie/12?zh-TW") != 1 || requestCount(counts, "/3/movie/12?ja-JP") != 1 {
		t.Fatal("languages were merged or not coalesced")
	}
}

func TestCoalescedErrorsAreNotCached(t *testing.T) {
	c := testMovieClient(t)
	release := make(chan struct{})
	var calls atomic.Int32
	status := 503
	c.fetch = func(ctx context.Context, _ string, _ int64) (outbound.Response, error) {
		calls.Add(1)
		<-release
		return outbound.Response{Status: status, Body: []byte(movieBody)}, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Go(func() {
			if _, err := c.Movie(context.Background(), 12, "en-US"); !errors.Is(err, ErrUnavailable) {
				t.Errorf("error=%v", err)
			}
		})
	}
	waitFlightWaiters(t, &c.movieFlights, candidateKey{12, "en-US"}, 4)
	close(release)
	wg.Wait()
	// One flight, three attempts of the existing retry policy.
	if calls.Load() != 3 || flightLen(&c.movieFlights) != 0 {
		t.Fatalf("calls=%d", calls.Load())
	}
	if _, ok := c.movies.get(candidateKey{12, "en-US"}, c.now()); ok {
		t.Fatal("error result cached")
	}
	status = 200
	if _, err := c.Movie(context.Background(), 12, "en-US"); err != nil || calls.Load() != 4 {
		t.Fatal("failed flight blocked a fresh request")
	}
}

func TestCancelledWaiterDoesNotAffectOthers(t *testing.T) {
	c := testMovieClient(t)
	release := make(chan struct{})
	var fetchCancelled atomic.Bool
	var calls atomic.Int32
	c.fetch = func(ctx context.Context, _ string, _ int64) (outbound.Response, error) {
		calls.Add(1)
		select {
		case <-release:
			return outbound.Response{Status: 200, Body: []byte(seasonBody)}, nil
		case <-ctx.Done():
			fetchCancelled.Store(true)
			return outbound.Response{}, ctx.Err()
		}
	}
	key := episodeKey{12, 0, 0, "zh-TW"}
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderDone := make(chan error, 1)
	go func() { _, err := c.Season(leaderCtx, 12, 0, "zh-TW"); leaderDone <- err }()
	waitFlightWaiters(t, &c.seasonFlights, key, 1)
	shortCtx, cancelShort := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelShort()
	shortDone := make(chan error, 1)
	go func() { _, err := c.Season(shortCtx, 12, 0, "zh-TW"); shortDone <- err }()
	followerDone := make(chan domain.SeasonCandidate, 1)
	go func() {
		value, err := c.Season(context.Background(), 12, 0, "zh-TW")
		if err != nil {
			t.Error(err)
		}
		followerDone <- value
	}()
	waitFlightWaiters(t, &c.seasonFlights, key, 3)
	cancelLeader()
	if err := <-leaderDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader error=%v", err)
	}
	if err := <-shortDone; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("short deadline error=%v", err)
	}
	waitFlightWaiters(t, &c.seasonFlights, key, 1)
	if fetchCancelled.Load() {
		t.Fatal("leaving waiters cancelled the shared fetch")
	}
	close(release)
	value := <-followerDone
	if value.Title != "Specials" || len(value.Episodes) != 1 || calls.Load() != 1 {
		t.Fatalf("follower=%+v calls=%d", value, calls.Load())
	}
	// Each waiter receives its own episode slice; the cache stays intact.
	value.Episodes[0].Title = "mutated"
	again, err := c.Season(context.Background(), 12, 0, "zh-TW")
	if err != nil || again.Episodes[0].Title != "Special" || calls.Load() != 1 {
		t.Fatal("coalesced season aliased the cache")
	}
}

func TestAbandonedFlightIsCancelledAndNotCached(t *testing.T) {
	c := testMovieClient(t)
	fetchCancelled := make(chan struct{})
	var calls atomic.Int32
	c.fetch = func(ctx context.Context, _ string, _ int64) (outbound.Response, error) {
		if calls.Add(1) > 1 {
			return outbound.Response{Status: 200, Body: []byte(movieBody)}, nil
		}
		<-ctx.Done()
		close(fetchCancelled)
		return outbound.Response{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := c.Movie(ctx, 12, "en-US"); done <- err }()
	}
	waitFlightWaiters(t, &c.movieFlights, candidateKey{12, "en-US"}, 2)
	cancel()
	for i := 0; i < 2; i++ {
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
	}
	select {
	case <-fetchCancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("abandoned provider request kept running")
	}
	// A new caller starts a fresh flight instead of joining the cancelled one.
	if _, err := c.Movie(context.Background(), 12, "en-US"); err != nil || calls.Load() != 2 {
		t.Fatalf("fresh flight error=%v calls=%d", err, calls.Load())
	}
}

func TestFlightPublishesOnlyForLiveWaiters(t *testing.T) {
	var g flightGroup[int, int]
	ctx, cancel := context.WithCancel(context.Background())
	published := 0
	_, err := g.do(ctx, 1, time.Second, func(context.Context) (int, error) {
		cancel() // the only waiter leaves exactly as the fetch completes
		return 7, nil
	}, func(int) { published++ })
	if !errors.Is(err, context.Canceled) || published != 0 {
		t.Fatalf("error=%v published=%d", err, published)
	}
	value, err := g.do(context.Background(), 1, time.Second, func(context.Context) (int, error) { return 8, nil }, func(int) { published++ })
	if err != nil || value != 8 || published != 1 || len(g.flights) != 0 {
		t.Fatal("successful flight was not published once")
	}
	_, err = g.do(context.Background(), 1, time.Second, func(context.Context) (int, error) { return 0, ErrUnavailable }, func(int) { published++ })
	if !errors.Is(err, ErrUnavailable) || published != 1 {
		t.Fatal("failed flight was published")
	}
}
