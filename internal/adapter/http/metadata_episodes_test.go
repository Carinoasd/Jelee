package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestSeasonEpisodeHTTPAuthorizationAndCanonicalNumbers(t *testing.T) {
	for _, tc := range []struct {
		name, role, path string
		status           int
	}{
		{"anonymous", "", "12/seasons/0", 401}, {"user", "u", "12/seasons/0", 403}, {"anonymous_episode", "", "12/seasons/0/episodes/1", 401}, {"user_episode", "u", "12/seasons/0/episodes/1", 403}, {"specials", "a", "12/seasons/0?language=zh-TW", 200}, {"episode", "a", "12/seasons/0/episodes/1?language=ja-JP", 200},
		{"zero_id", "a", "0/seasons/0", 400}, {"negative_season", "a", "12/seasons/-1", 400}, {"noncanonical_season", "a", "12/seasons/00", 400}, {"overflow_season", "a", "12/seasons/2147483648", 400}, {"zero_episode", "a", "12/seasons/0/episodes/0", 400}, {"negative_episode", "a", "12/seasons/0/episodes/-1", 400}, {"noncanonical_episode", "a", "12/seasons/0/episodes/01", 400}, {"overflow_episode", "a", "12/seasons/0/episodes/2147483648", 400},
		{"language", "a", "12/seasons/0?language=fr-FR", 400}, {"empty_language", "a", "12/seasons/0?language=", 400}, {"duplicate", "a", "12/seasons/0?language=en-US&language=zh-TW", 400}, {"unknown_query", "a", "12/seasons/0/episodes/1?api_key=bad", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &httpMovieProvider{}
			h, _ := metadataFixture(t, p)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, accountRequest("GET", "/api/v1/metadata/tmdb/series/"+tc.path, "", tc.role))
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if (tc.status == 200 && p.calls != 1) || (tc.status != 200 && p.calls != 0) {
				t.Fatal("invalid/nonadmin request reached provider")
			}
			if tc.name == "specials" {
				var body struct {
					Data domain.SeasonCandidate `json:"data"`
				}
				if json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Data.SeriesID != 12 || body.Data.SeasonNumber != 0 || body.Data.Episodes == nil || p.language != "zh-TW" {
					t.Fatal("season tuple lost")
				}
			}
			if tc.name == "episode" {
				var body struct {
					Data domain.EpisodeCandidate `json:"data"`
				}
				if json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Data.SeriesID != 12 || body.Data.SeasonNumber != 0 || body.Data.EpisodeNumber != 1 || p.language != "ja-JP" {
					t.Fatal("episode tuple lost")
				}
			}
		})
	}
}

func TestSeasonEpisodeHTTPFailureAndSpecification(t *testing.T) {
	p := &httpMovieProvider{fail: domain.ErrNotFound}
	h, cfg := metadataFixture(t, p)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, accountRequest("GET", "/api/v1/metadata/tmdb/series/12/seasons/0/episodes/1", "", "a"))
	if w.Code != 404 || !strings.Contains(w.Body.String(), "not_found") {
		t.Fatal("episode not found differs")
	}
	paths := []string{"/api/v1/metadata/tmdb/series/{id}/seasons/{season}", "/api/v1/metadata/tmdb/series/{id}/seasons/{season}/episodes/{episode}"}
	spec := Specification(cfg)
	for _, path := range paths {
		if spec["paths"].(map[string]any)[path] == nil {
			t.Fatal("season/episode specification absent")
		}
	}
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	props := schemas["SeasonCandidate"].(map[string]any)["properties"].(map[string]any)
	if props["episodes"].(map[string]any)["maxItems"] != domain.MaxMetadataSeasonEpisodes {
		t.Fatal("season count limit missing")
	}
	if schemas["MovieCandidate"].(map[string]any)["properties"].(map[string]any)["releaseDate"] == nil {
		t.Fatal("movie schema overwritten")
	}
	cfg.TMDBAPIKey = ""
	for _, path := range paths {
		if Specification(cfg)["paths"].(map[string]any)[path] != nil {
			t.Fatal("disabled season/episode advertised")
		}
	}
	base := newAccountHTTPFixture(t, httpAccountRepository{}, nil)
	if w := base.serve(accountRequest("GET", "/api/v1/metadata/tmdb/series/12/seasons/0", "", "a")); w.Code != 404 {
		t.Fatal("disabled season exposed")
	}
}
