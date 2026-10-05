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

const findBody = `{"movie_results":[{"id":12,"title":"電影","original_title":"Movie","overview":"Summary","release_date":"2024-02-29","poster_path":"/x.jpg"}],` +
	`"tv_results":[{"id":34,"name":"劇集","original_name":"Series","first_air_date":"2020-01-01"}],` +
	`"person_results":[{"id":5,"name":"Person"}],"tv_episode_results":[{"id":6}],"tv_season_results":[]}`

func TestFindByExternalIDRequestAndMapping(t *testing.T) {
	c := testMovieClient(t)
	now := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	for _, tc := range []struct{ source, id string }{
		{domain.ExternalSourceIMDb, "tt0137523"}, {domain.ExternalSourceTVDB, "81189"}, {domain.ExternalSourceWikidata, "Q190050"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			calls := 0
			c.fetch = func(ctx context.Context, raw string, limit int64) (outbound.Response, error) {
				calls++
				u, err := url.Parse(raw)
				q := u.Query()
				if err != nil || u.Scheme != "https" || u.Host != "api.themoviedb.org" || u.User != nil || u.Path != "/3/find/"+tc.id || u.RawPath != "" ||
					q.Get("api_key") != testKey || q.Get("external_source") != tc.source || q.Get("language") != "ja-JP" || len(q) != 3 || limit != 1<<20 {
					t.Fatalf("find request contract differs: %s", strings.ReplaceAll(raw, testKey, "KEY"))
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("no total deadline")
				}
				return outbound.Response{Status: 200, Body: []byte(findBody)}, nil
			}
			value, err := c.FindByExternalID(context.Background(), tc.source, tc.id, "ja-JP")
			if err != nil || calls != 1 || value.ExternalSource != tc.source || value.ExternalID != tc.id || value.Language != "ja-JP" || !value.FetchedAt.Equal(now) {
				t.Fatalf("value=%+v error=%v", value, err)
			}
			if len(value.Movies) != 1 || len(value.Series) != 1 {
				t.Fatal("candidate lists differ")
			}
			movie, series := value.Movies[0], value.Series[0]
			if movie.ProviderID != 12 || movie.Title != "電影" || movie.Source != "TMDB" || movie.SourceURL != "https://www.themoviedb.org/movie/12" || movie.Language != "ja-JP" || movie.ReleaseDate != "2024-02-29" {
				t.Fatalf("movie=%+v", movie)
			}
			if series.ProviderID != 34 || series.Title != "劇集" || series.SourceURL != "https://www.themoviedb.org/tv/34" || series.FirstAirDate != "2020-01-01" || !series.FetchedAt.Equal(now) {
				t.Fatalf("series=%+v", series)
			}
		})
	}
}

func TestFindByExternalIDEmptyResultIsCached(t *testing.T) {
	c := testMovieClient(t)
	calls := 0
	c.fetch = func(context.Context, string, int64) (outbound.Response, error) {
		calls++
		return outbound.Response{Status: 200, Body: []byte(`{"movie_results":[],"tv_results":[],"person_results":[]}`)}, nil
	}
	for i := 0; i < 2; i++ {
		value, err := c.FindByExternalID(context.Background(), domain.ExternalSourceIMDb, "tt9999999", "en-US")
		if err != nil || value.Movies == nil || value.Series == nil || len(value.Movies)+len(value.Series) != 0 {
			t.Fatalf("value=%+v error=%v", value, err)
		}
	}
	if calls != 1 {
		t.Fatal("empty answer was not cached")
	}
}

func TestFindByExternalIDRejectsInvalidInput(t *testing.T) {
	c := testMovieClient(t)
	c.fetch = func(context.Context, string, int64) (outbound.Response, error) {
		t.Fatal("invalid request reached provider")
		return outbound.Response{}, nil
	}
	for _, tc := range []struct{ source, id, language string }{
		{"imdb_id", "", "en-US"}, {"imdb_id", "tt", "en-US"}, {"imdb_id", "tt123456", "en-US"}, {"imdb_id", "tt12345678901", "en-US"},
		{"imdb_id", "TT0137523", "en-US"}, {"imdb_id", "nm0000093", "en-US"}, {"imdb_id", "tt0137523 ", "en-US"}, {"imdb_id", " tt0137523", "en-US"},
		{"imdb_id", "tt0137523/../../movie/1", "en-US"}, {"imdb_id", "tt0137523?api_key=x", "en-US"}, {"imdb_id", "tt0137523%2F", "en-US"},
		{"imdb_id", "tt013752３", "en-US"}, {"imdb_id", "tt0137523\x00", "en-US"}, {"imdb_id", "tt0137523", ""}, {"imdb_id", "tt0137523", "fr-FR"},
		{"imdb_id", "tt0137523", "en-US&api_key=x"},
		{"tvdb_id", "0", "en-US"}, {"tvdb_id", "081189", "en-US"}, {"tvdb_id", "-1", "en-US"}, {"tvdb_id", "+1", "en-US"},
		{"tvdb_id", "2147483648", "en-US"}, {"tvdb_id", "99999999999", "en-US"}, {"tvdb_id", "1e3", "en-US"}, {"tvdb_id", "tt0137523", "en-US"},
		{"wikidata_id", "Q", "en-US"}, {"wikidata_id", "Q0", "en-US"}, {"wikidata_id", "Q012", "en-US"}, {"wikidata_id", "q42", "en-US"},
		{"wikidata_id", "P31", "en-US"}, {"wikidata_id", "Q12345678901", "en-US"},
		{"tmdb_id", "12", "en-US"}, {"facebook_id", "name", "en-US"}, {"IMDB_ID", "tt0137523", "en-US"}, {"", "tt0137523", "en-US"},
	} {
		if _, err := c.FindByExternalID(context.Background(), tc.source, tc.id, tc.language); err != domain.ErrInvalid {
			t.Fatalf("accepted source=%q id=%q language=%q error=%v", tc.source, tc.id, tc.language, err)
		}
	}
	for _, valid := range []struct{ source, id string }{
		{"imdb_id", "tt0000001"}, {"imdb_id", "tt1234567890"}, {"tvdb_id", "1"}, {"tvdb_id", "2147483647"}, {"wikidata_id", "Q1"}, {"wikidata_id", "Q1234567890"},
	} {
		if !domain.ValidExternalID(valid.source, valid.id) {
			t.Fatalf("rejected valid %s %q", valid.source, valid.id)
		}
	}
}

func TestFindByExternalIDResponseContract(t *testing.T) {
	var many []string
	for i := 1; i <= domain.MaxExternalIDResults+1; i++ {
		many = append(many, fmt.Sprintf(`{"id":%d,"title":"Movie"}`, i))
	}
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"ok", 200, findBody, nil},
		{"credentials", 401, "secret", ErrCredentials}, {"forbidden", 403, "", ErrCredentials},
		{"rate", 429, "", ErrRateLimited}, {"server", 503, "", ErrUnavailable}, {"not_found", 404, "", ErrUnavailable}, {"redirect", 302, "", ErrUnavailable},
		{"invalid_json", 200, "secret", ErrResponse}, {"missing_movies", 200, `{"tv_results":[]}`, ErrResponse},
		{"null_series", 200, `{"movie_results":[],"tv_results":null}`, ErrResponse},
		{"duplicate_movie", 200, `{"movie_results":[{"id":1,"title":"A"},{"id":1,"title":"B"}],"tv_results":[]}`, ErrResponse},
		{"duplicate_series", 200, `{"movie_results":[],"tv_results":[{"id":1,"name":"A"},{"id":1,"name":"B"}]}`, ErrResponse},
		{"too_many", 200, `{"movie_results":[` + strings.Join(many, ",") + `],"tv_results":[]}`, ErrResponse},
		{"bad_id", 200, `{"movie_results":[{"id":0,"title":"A"}],"tv_results":[]}`, ErrResponse},
		{"missing_series_name", 200, `{"movie_results":[],"tv_results":[{"id":3}]}`, ErrResponse},
		{"bad_date", 200, `{"movie_results":[],"tv_results":[{"id":3,"name":"A","first_air_date":"2023-02-29"}]}`, ErrResponse},
		{"key_echo", 200, `{"movie_results":[{"id":1,"title":"` + testKey + `"}],"tv_results":[]}`, ErrResponse},
		{"control", 200, `{"movie_results":[{"id":1,"title":"bad\u0000"}],"tv_results":[]}`, ErrResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testMovieClient(t)
			calls := 0
			c.fetch = func(context.Context, string, int64) (outbound.Response, error) {
				calls++
				return outbound.Response{Status: tc.status, Body: []byte(tc.body)}, nil
			}
			_, err := c.FindByExternalID(context.Background(), domain.ExternalSourceIMDb, "tt0137523", "en-US")
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v", err)
			}
			if err != nil && (strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), "secret")) {
				t.Fatal("secret leaked")
			}
			first := calls
			_, err = c.FindByExternalID(context.Background(), domain.ExternalSourceIMDb, "tt0137523", "en-US")
			if !errors.Is(err, tc.want) || (tc.want != nil && calls != first*2) || (tc.want == nil && calls != first) {
				t.Fatal("unexpected cache policy")
			}
		})
	}
}

func TestFindByExternalIDRetriesAndGovernor(t *testing.T) {
	c := testMovieClient(t)
	c.governor = newRequestGovernor()
	c.governor.interval = 0
	var governed []time.Duration
	c.governor.wait = func(_ context.Context, d time.Duration) error {
		governed = append(governed, d)
		c.governor.mu.Lock()
		c.governor.cooldown = time.Time{}
		c.governor.mu.Unlock()
		return nil
	}
	var retried []time.Duration
	c.wait = func(_ context.Context, d time.Duration) error { retried = append(retried, d); return nil }
	statuses := []int{429, 200}
	calls := 0
	c.fetch = func(context.Context, string, int64) (outbound.Response, error) {
		status := statuses[calls]
		calls++
		r := outbound.Response{Status: status, Body: []byte(findBody)}
		if status == 429 {
			r.RetryAfter = "2"
		}
		return r, nil
	}
	value, err := c.FindByExternalID(context.Background(), domain.ExternalSourceTVDB, "81189", "en-US")
	if err != nil || calls != 2 || len(value.Movies) != 1 {
		t.Fatalf("value=%+v error=%v calls=%d", value, err, calls)
	}
	if len(retried) != 1 || retried[0] < 2*time.Second || len(governed) != 1 || governed[0] <= time.Second {
		t.Fatalf("retry=%v governor=%v", retried, governed)
	}

	// Exhausted rate limiting is reported and never cached.
	c.governor = nil
	calls = 0
	statuses = []int{429, 429, 429, 200}
	if _, err := c.FindByExternalID(context.Background(), domain.ExternalSourceTVDB, "81190", "en-US"); !errors.Is(err, ErrRateLimited) || calls != 3 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
	if _, err := c.FindByExternalID(context.Background(), domain.ExternalSourceTVDB, "81190", "en-US"); err != nil || calls != 4 {
		t.Fatal("rate-limited answer was cached")
	}

	// Transport failures are sanitized and preserve cancellation.
	for _, tc := range []struct{ source, want error }{{errors.New("https://secret@10.0.0.1/?api_key=" + testKey), ErrUnavailable}, {context.DeadlineExceeded, context.DeadlineExceeded}} {
		c.fetch = func(context.Context, string, int64) (outbound.Response, error) { return outbound.Response{}, tc.source }
		if _, err := c.FindByExternalID(context.Background(), domain.ExternalSourceTVDB, "81191", "en-US"); !errors.Is(err, tc.want) {
			t.Fatalf("unsafe error=%v", err)
		}
	}
}

func TestFindByExternalIDCacheRules(t *testing.T) {
	c := testMovieClient(t)
	now := time.Now().UTC()
	c.now = func() time.Time { return now }
	calls := 0
	c.fetch = func(context.Context, string, int64) (outbound.Response, error) {
		calls++
		return outbound.Response{Status: 200, Body: []byte(findBody)}, nil
	}
	find := func(source, id, language string) domain.ExternalIDMatches {
		t.Helper()
		value, err := c.FindByExternalID(context.Background(), source, id, language)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	first := find(domain.ExternalSourceIMDb, "tt0137523", "zh-TW")
	first.Movies[0].Title = "mutated first return"
	first.Series = append(first.Series[:0], domain.SeriesCandidate{Title: "replaced"})
	now = now.Add(23 * time.Hour)
	hit := find(domain.ExternalSourceIMDb, "tt0137523", "zh-TW")
	if calls != 1 || hit.Movies[0].Title != "電影" || hit.Series[0].Title != "劇集" {
		t.Fatal("cache insertion aliased candidates")
	}
	hit.Movies[0].Title = "mutated cache hit"
	if again := find(domain.ExternalSourceIMDb, "tt0137523", "zh-TW"); calls != 1 || again.Movies[0].Title != "電影" || !again.FetchedAt.Equal(now.Add(-23*time.Hour)) {
		t.Fatal("cache hit aliased candidates or extended TTL")
	}
	find(domain.ExternalSourceIMDb, "tt0137523", "ja-JP")
	if calls != 2 {
		t.Fatal("language partition collision")
	}
	find(domain.ExternalSourceWikidata, "Q190050", "zh-TW")
	find(domain.ExternalSourceTVDB, "190050", "zh-TW")
	if calls != 4 {
		t.Fatal("source partition collision")
	}
	now = now.Add(time.Hour)
	find(domain.ExternalSourceIMDb, "tt0137523", "zh-TW")
	if calls != 5 {
		t.Fatal("expired answer reused")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.FindByExternalID(ctx, domain.ExternalSourceIMDb, "tt0137523", "zh-TW"); !errors.Is(err, context.Canceled) || calls != 5 {
		t.Fatal("cancelled cache hit accepted")
	}
	for id := 1; id <= movieCacheCapacity+1; id++ {
		c.external.put(externalKey{domain.ExternalSourceTVDB, fmt.Sprint(id), "en-US"}, domain.ExternalIDMatches{FetchedAt: now})
	}
	if len(c.external.entries) != movieCacheCapacity || c.external.lru.Len() != movieCacheCapacity {
		t.Fatal("external cache exceeds capacity")
	}
}
