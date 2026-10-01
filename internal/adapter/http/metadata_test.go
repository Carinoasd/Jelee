package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

type httpMovieProvider struct {
	preferences app.MetadataPreferencesRepository
	calls       int
	fail        error
	language    string
}

func (p *httpMovieProvider) Season(ctx context.Context, series, season int32, language string) (domain.SeasonCandidate, error) {
	p.calls++
	p.language = language
	if err := ctx.Err(); err != nil {
		return domain.SeasonCandidate{}, err
	}
	return domain.SeasonCandidate{ProviderID: 500, SeriesID: series, SeasonNumber: season, Language: language, Overview: "Summary", Episodes: []domain.EpisodeCandidate{}}, p.fail
}
func (p *httpMovieProvider) Episode(ctx context.Context, series, season, episode int32, language string) (domain.EpisodeCandidate, error) {
	p.calls++
	p.language = language
	if err := ctx.Err(); err != nil {
		return domain.EpisodeCandidate{}, err
	}
	return domain.EpisodeCandidate{ProviderID: 900, SeriesID: series, SeasonNumber: season, EpisodeNumber: episode, Language: language, Overview: "Summary"}, p.fail
}

func (p *httpMovieProvider) Series(ctx context.Context, id int32, language string) (domain.SeriesCandidate, error) {
	p.calls++
	p.language = language
	if err := ctx.Err(); err != nil {
		return domain.SeriesCandidate{}, err
	}
	return domain.SeriesCandidate{ProviderID: id, Title: "劇集", Overview: "Summary", Language: language, FirstAirDate: "2024-01-01"}, p.fail
}
func (p *httpMovieProvider) SearchSeries(ctx context.Context, input domain.SeriesSearchInput) ([]domain.SeriesCandidate, error) {
	p.calls++
	p.language = input.Language
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []domain.SeriesCandidate{{ProviderID: 12, Title: input.Query, Language: input.Language, FirstAirDate: "2024-01-01"}}, p.fail
}

func (p *httpMovieProvider) SearchMovies(ctx context.Context, input domain.MovieSearchInput) ([]domain.MovieCandidate, error) {
	p.calls++
	p.language = input.Language
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []domain.MovieCandidate{{ProviderID: 12, Title: input.Query, Language: input.Language, Source: "TMDB", ReleaseDate: "2024-02-29", FetchedAt: time.Now().UTC()}}, p.fail
}

func (p *httpMovieProvider) Movie(ctx context.Context, id int32, language string) (domain.MovieCandidate, error) {
	p.calls++
	p.language = language
	if err := ctx.Err(); err != nil {
		return domain.MovieCandidate{}, err
	}
	return domain.MovieCandidate{ProviderID: id, Title: "電影", Overview: "Summary", Language: language, Source: "TMDB", SourceURL: "https://www.themoviedb.org/movie/12", FetchedAt: time.Now().UTC()}, p.fail
}

func metadataFixture(t *testing.T, p *httpMovieProvider, locales ...string) (http.Handler, config.Config) {
	t.Helper()
	cfg := validConfig()
	cfg.EnableAccounts = true
	cfg.Accounts = config.DefaultAccountsConfig()
	cfg.TMDBAPIKey = strings.Repeat("a", 32)
	locale := ""
	if len(locales) > 0 {
		locale = locales[0]
	}
	backend := &fakeBackend{auth: func(_ context.Context, token string) (access.Principal, error) {
		if token != strings.Repeat("a", 43) && token != strings.Repeat("u", 43) {
			return access.Principal{}, domain.ErrUnauthenticated
		}
		return access.Principal{UserID: userID, SessionID: sessionID, Kind: access.ClientWeb, Admin: token == strings.Repeat("a", 43), Locale: locale}, nil
	}}
	accounts, err := app.NewAccounts(httpAccountRepository{}, &httpAccountPasswords{}, app.AccountOptions{SessionTTL: 24 * time.Hour, MaxSessions: 8, LockAfter: 5, LockFor: 15 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	service, err := app.NewMetadata(p)
	if err != nil {
		t.Fatal(err)
	}
	if p.preferences != nil {
		service, err = service.WithLibraryPreferences(p.preferences)
		if err != nil {
			t.Fatal(err)
		}
	}
	handler, err := NewWithJobs(cfg, backend, app.NewCatalog(&fakeRepository{}), &fakeResolver{}, slog.New(slog.NewTextHandler(io.Discard, nil)), accounts, nil, service)
	if err != nil {
		t.Fatal(err)
	}
	return handler, cfg
}

func TestMetadataPreviewAuthorizationAndInput(t *testing.T) {
	for _, tc := range []struct {
		name, role, path string
		status           int
	}{
		{"anonymous", "", "12", 401}, {"user", "u", "12", 403}, {"admin", "a", "12", 200}, {"language", "a", "12?language=ja-JP", 200},
		{"zero", "a", "0", 400}, {"negative", "a", "-1", 400}, {"plus", "a", "%2B12", 400}, {"noncanonical", "a", "012", 400}, {"overflow", "a", "2147483648", 400},
		{"empty_language", "a", "12?language=", 400}, {"unsupported", "a", "12?language=fr-FR", 400}, {"duplicate", "a", "12?language=en-US&language=zh-TW", 400}, {"unknown_query", "a", "12?api_key=secret", 400}, {"malformed_query", "a", "12?language=%ZZ", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &httpMovieProvider{}
			h, _ := metadataFixture(t, p)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, accountRequest("GET", "/api/v1/metadata/tmdb/movies/"+tc.path, "", tc.role))
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if (tc.status == 200 && p.calls != 1) || (tc.status != 200 && p.calls != 0) {
				t.Fatal("unauthorized/invalid request reached provider")
			}
			if tc.status == 200 {
				var body struct {
					Data domain.MovieCandidate `json:"data"`
				}
				if json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Data.ProviderID != 12 || body.Data.Title != "電影" {
					t.Fatal("candidate envelope differs")
				}
				want := "zh-CN"
				if tc.name == "language" {
					want = "ja-JP"
				}
				if p.language != want {
					t.Fatal("language differs")
				}
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("private response can be shared cached")
			}
		})
	}
}

func TestMetadataPreviewSafeErrorsAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{{"unsafe", errors.New("secret://10.0.0.1/password"), 503, "metadata_unavailable"}, {"missing", domain.ErrNotFound, 404, "not_found"}, {"timeout", context.DeadlineExceeded, 408, "request_timeout"}, {"cancelled", context.Canceled, 408, "request_timeout"}} {
		t.Run(tc.name, func(t *testing.T) {
			p := &httpMovieProvider{fail: tc.err}
			h, _ := metadataFixture(t, p)
			for _, language := range []string{"zh-CN", "zh-TW", "ja-JP", "en-US"} {
				r := accountRequest("GET", "/api/v1/metadata/tmdb/movies/12", "", "a")
				r.Header.Set("Accept-Language", language)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) || strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "10.0.0.1") || w.Header().Get("Content-Language") != language {
					t.Fatalf("unsafe response %d %s", w.Code, w.Body.String())
				}
			}
		})
	}
	p := &httpMovieProvider{}
	h, _ := metadataFixture(t, p)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, accountRequest("GET", "/api/v1/metadata/tmdb/movies/12", "", "a").WithContext(ctx))
	if w.Code != 408 || p.calls != 0 {
		t.Fatal("cancelled request reached provider")
	}
}

func TestMetadataPreviewRolloutSpecificationAndCredits(t *testing.T) {
	p := &httpMovieProvider{}
	h, cfg := metadataFixture(t, p)
	path := "/api/v1/metadata/tmdb/movies/{id}"
	spec := Specification(cfg)
	if _, ok := spec["paths"].(map[string]any)[path]; !ok {
		t.Fatal("enabled route missing from specification")
	}
	for _, change := range []func(*config.Config){func(c *config.Config) { c.EnableAccounts = false }, func(c *config.Config) { c.TMDBAPIKey = "" }} {
		disabled := cfg
		change(&disabled)
		if _, ok := Specification(disabled)["paths"].(map[string]any)[path]; ok {
			t.Fatal("disabled route advertised")
		}
	}
	if _, err := NewWithJobs(cfg, &fakeBackend{}, app.NewCatalog(&fakeRepository{}), &fakeResolver{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil); err == nil {
		t.Fatal("half configured metadata enabled")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, accountRequest("GET", "/api-docs", "", ""))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "This product uses the TMDB API but is not endorsed or certified by TMDB.") || !strings.Contains(w.Body.String(), `aria-label="Credits"`) || !strings.Contains(w.Body.String(), "blue_short-") || !strings.Contains(w.Header().Get("Content-Security-Policy"), "img-src https://www.themoviedb.org") {
		t.Fatal("TMDB credits unavailable")
	}
	// Default rollout still has no preview route.
	base := newAccountHTTPFixture(t, httpAccountRepository{}, nil)
	w = base.serve(accountRequest("GET", "/api/v1/metadata/tmdb/movies/12", "", "a"))
	if w.Code != 404 {
		t.Fatal("unconfigured preview exposed")
	}
}
