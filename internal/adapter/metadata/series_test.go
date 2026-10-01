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

const seriesBody = `{"id":12,"name":"劇集","original_name":"Series","overview":"Summary","first_air_date":"2024-02-29","homepage":"http://127.0.0.1/secret"}`

func TestSeriesDetailsCacheIsolationExpiryAndCancellation(t *testing.T) {
	c := testMovieClient(t)
	calls := 0
	now := time.Now().UTC()
	c.now = func() time.Time { return now }
	c.fetch = func(ctx context.Context, raw string, limit int64) (outbound.Response, error) {
		calls++
		u, _ := url.Parse(raw)
		if limit != 1<<20 || u.Query().Get("api_key") != testKey || !domain.ValidMetadataLanguage(u.Query().Get("language")) {
			t.Fatal("series request differs")
		}
		if u.Path == "/3/movie/12" {
			return outbound.Response{Status: 200, Body: []byte(movieBody)}, nil
		}
		if u.Path != "/3/tv/12" {
			t.Fatal("series endpoint differs")
		}
		return outbound.Response{Status: 200, Body: []byte(seriesBody)}, nil
	}
	value, err := c.Series(context.Background(), 12, "zh-TW")
	if err != nil || value.SourceURL != "https://www.themoviedb.org/tv/12" || value.FirstAirDate != "2024-02-29" {
		t.Fatalf("candidate=%+v error=%v", value, err)
	}
	value.Title = "mutated"
	if _, err := c.Movie(context.Background(), 12, "zh-TW"); err != nil {
		t.Fatal(err)
	}
	cached, err := c.Series(context.Background(), 12, "zh-TW")
	if err != nil || cached.Title != "劇集" || calls != 2 {
		t.Fatal("series/movie cache collision or mutated value")
	}
	now = now.Add(23 * time.Hour)
	if _, err := c.Series(context.Background(), 12, "zh-TW"); err != nil || calls != 2 {
		t.Fatal("early expiry")
	}
	now = now.Add(time.Hour)
	if _, err := c.Series(context.Background(), 12, "zh-TW"); err != nil || calls != 3 {
		t.Fatal("TTL refreshed on hit")
	}
	if _, err := c.Series(context.Background(), 12, "ja-JP"); err != nil || calls != 4 {
		t.Fatal("locale cache collision")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Series(ctx, 12, "zh-TW"); err != context.Canceled {
		t.Fatal("cancelled cache query accepted")
	}
	var cache seriesCache
	for id := int32(1); id <= 257; id++ {
		cache.put(candidateKey{id, "en-US"}, domain.SeriesCandidate{ProviderID: id, FetchedAt: now})
	}
	if len(cache.entries) != 256 {
		t.Fatal("series cache unbounded")
	}
	if _, ok := cache.get(candidateKey{1, "en-US"}, now); ok {
		t.Fatal("old series not evicted")
	}
}

func TestSeriesProviderContracts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"valid", 200, seriesBody, nil}, {"missing", 404, "secret", domain.ErrNotFound}, {"credentials", 401, "secret", ErrCredentials}, {"rate", 429, "", ErrRateLimited}, {"server", 500, "", ErrUnavailable},
		{"wrong_id", 200, strings.Replace(seriesBody, `"id":12`, `"id":13`, 1), ErrResponse}, {"bad_date", 200, strings.Replace(seriesBody, "2024-02-29", "2023-02-29", 1), ErrResponse}, {"blank", 200, `{"id":12,"name":" "}`, ErrResponse}, {"secret", 200, `{"id":12,"name":"` + testKey + `"}`, ErrResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testMovieClient(t)
			calls := 0
			c.fetch = func(context.Context, string, int64) (outbound.Response, error) {
				calls++
				return outbound.Response{Status: tc.status, Body: []byte(tc.body)}, nil
			}
			_, err := c.Series(context.Background(), 12, "en-US")
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v", err)
			}
			first := calls
			_, err = c.Series(context.Background(), 12, "en-US")
			if !errors.Is(err, tc.want) || (tc.want != nil && calls != first*2) || (tc.want == nil && calls != first) {
				t.Fatal("series error cached")
			}
		})
	}
	c := testMovieClient(t)
	c.fetch = func(context.Context, string, int64) (outbound.Response, error) {
		t.Fatal("invalid series query reached provider")
		return outbound.Response{}, nil
	}
	if _, err := c.Series(context.Background(), 0, "en-US"); err != domain.ErrInvalid {
		t.Fatal("invalid id accepted")
	}
	if _, err := c.Series(context.Background(), 12, "fr-FR"); err != domain.ErrInvalid {
		t.Fatal("invalid language accepted")
	}
}

func TestSeriesSearchYearParameterAndBounds(t *testing.T) {
	c := testMovieClient(t)
	body := `{"page":1,"results":[` + seriesBody + `]}`
	c.fetch = func(ctx context.Context, raw string, limit int64) (outbound.Response, error) {
		u, _ := url.Parse(raw)
		q := u.Query()
		if u.Path != "/3/search/tv" || q.Get("first_air_date_year") != "2024" || q.Get("year") != "" || q.Get("query") != "Series & key=bad" || q.Get("include_adult") != "false" || q.Get("api_key") != testKey || q.Get("language") != "zh-TW" || q.Get("page") != "1" || limit != 1<<20 {
			t.Fatal("TV first-air-date search differs")
		}
		return outbound.Response{Status: 200, Body: []byte(body)}, nil
	}
	result, err := c.SearchSeries(context.Background(), domain.SeriesSearchInput{Query: "Series & key=bad", Year: 2024, Language: "zh-TW"})
	if err != nil || len(result) != 1 || result[0].FirstAirDate != "2024-02-29" {
		t.Fatal("series search failed")
	}
	var many []string
	for id := 100; id < 121; id++ {
		many = append(many, fmt.Sprintf(`{"id":%d,"name":"Series"}`, id))
	}
	for _, invalid := range []string{`{}`, `{"page":2,"results":[]}`, `{"page":1,"results":null}`, `{"page":1,"results":[` + seriesBody + `,` + seriesBody + `]}`, `{"page":1,"results":[` + strings.Join(many, ",") + `]}`} {
		c.fetch = func(context.Context, string, int64) (outbound.Response, error) {
			return outbound.Response{Status: 200, Body: []byte(invalid)}, nil
		}
		if _, err := c.SearchSeries(context.Background(), domain.SeriesSearchInput{Query: "Series", Language: "en-US"}); err != ErrResponse {
			t.Fatal("invalid results accepted")
		}
	}
	for _, source := range []error{errors.New("secret://10.0.0.1"), context.Canceled, context.DeadlineExceeded} {
		c.fetch = func(context.Context, string, int64) (outbound.Response, error) { return outbound.Response{}, source }
		_, err := c.Series(context.Background(), 13, "en-US")
		if source == context.Canceled || source == context.DeadlineExceeded {
			if !errors.Is(err, source) {
				t.Fatal("cancellation lost")
			}
		} else if err != ErrUnavailable {
			t.Fatal("unsafe error leaked")
		}
	}
}
