package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestSeriesHTTPAuthorityAndValidation(t *testing.T) {
	for _, tc := range []struct {
		name, role, path string
		status           int
	}{
		{"anonymous", "", "/12", 401}, {"user", "u", "/12", 403}, {"detail", "a", "/12?language=zh-TW", 200}, {"search", "a", "?query=Series&year=2024&language=ja-JP", 200},
		{"zero", "a", "/0", 400}, {"overflow", "a", "/2147483648", 400}, {"noncanonical", "a", "/012", 400}, {"unknown_detail_query", "a", "/12?query=bad", 400},
		{"blank_search", "a", "?query=%20", 400}, {"duplicate", "a", "?query=a&query=b", 400}, {"year_empty", "a", "?query=Series&year=", 400}, {"year_sign", "a", "?query=Series&year=%2B2024", 400}, {"year_zero", "a", "?query=Series&year=0000", 400}, {"unknown_query", "a", "?query=Series&api_key=bad", 400}, {"unknown_language", "a", "/12?language=fr-FR", 400}, {"empty_language", "a", "?query=Series&language=", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &httpMovieProvider{}
			h, _ := metadataFixture(t, p)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, accountRequest("GET", "/api/v1/metadata/tmdb/series"+tc.path, "", tc.role))
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if (tc.status == 200 && p.calls != 1) || (tc.status != 200 && p.calls != 0) {
				t.Fatal("invalid/nonadmin request reached provider")
			}
			if tc.name == "search" {
				var body struct {
					Data domain.SeriesMatches `json:"data"`
				}
				if json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Data.Candidates) != 1 || !body.Data.Candidates[0].NeedsConfirmation || !body.Data.Candidates[0].ExactTitle || !body.Data.Candidates[0].ExactYear || p.language != "ja-JP" {
					t.Fatal("series match differs")
				}
			}
			if tc.name == "detail" {
				var body struct {
					Data domain.SeriesCandidate `json:"data"`
				}
				if json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Data.ProviderID != 12 || body.Data.FirstAirDate != "2024-01-01" || p.language != "zh-TW" {
					t.Fatal("series detail differs")
				}
			}
		})
	}
}

func TestSeriesHTTPFailuresRolloutAndSchemaIsolation(t *testing.T) {
	p := &httpMovieProvider{fail: domain.ErrMetadataUnavailable}
	h, cfg := metadataFixture(t, p)
	for _, path := range []string{"/12", "?query=Series"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, accountRequest("GET", "/api/v1/metadata/tmdb/series"+path, "", "a"))
		if w.Code != 503 || !strings.Contains(w.Body.String(), "metadata_unavailable") {
			t.Fatal("failure differs")
		}
	}
	spec := Specification(cfg)
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	movie := schemas["MovieCandidate"].(map[string]any)["properties"].(map[string]any)
	series := schemas["SeriesCandidate"].(map[string]any)["properties"].(map[string]any)
	if movie["releaseDate"] == nil || movie["firstAirDate"] != nil || series["firstAirDate"] == nil || series["releaseDate"] != nil {
		t.Fatal("series schema mutated movie fields")
	}
	for _, path := range []string{"/api/v1/metadata/tmdb/series", "/api/v1/metadata/tmdb/series/{id}"} {
		if spec["paths"].(map[string]any)[path] == nil {
			t.Fatal("series route absent")
		}
	}
	cfg.TMDBAPIKey = ""
	for path := range Specification(cfg)["paths"].(map[string]any) {
		if strings.Contains(path, "/metadata/") {
			t.Fatal("disabled metadata advertised")
		}
	}
	base := newAccountHTTPFixture(t, httpAccountRepository{}, nil)
	if w := base.serve(accountRequest("GET", "/api/v1/metadata/tmdb/series/12", "", "a")); w.Code != 404 {
		t.Fatal("disabled series exposed")
	}
	p = &httpMovieProvider{}
	h, _ = metadataFixture(t, p)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, accountRequest("GET", "/api/v1/metadata/tmdb/series/12", "", "a").WithContext(ctx))
	if w.Code != 408 || p.calls != 0 {
		t.Fatal("cancelled request reached series provider")
	}
}
