package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestMovieSearchHTTPAuthorityAndValidation(t *testing.T) {
	for _, tc := range []struct {
		name, role, query string
		status            int
	}{
		{"anonymous", "", "query=Movie", 401}, {"user", "u", "query=Movie", 403}, {"admin", "a", "query=Movie&year=2024&language=zh-TW", 200},
		{"no_year", "a", "query=Movie", 200}, {"trim", "a", "query=%20Movie%20", 200},
		{"missing", "a", "", 400}, {"empty", "a", "query=", 400}, {"blank", "a", "query=%20%20", 400}, {"controls", "a", "query=Movie%0Asecret", 400},
		{"long", "a", "query=" + strings.Repeat("x", 257), 400}, {"bad_utf8", "a", "query=%FF", 400}, {"bad_escape", "a", "query=%ZZ", 400},
		{"duplicate_query", "a", "query=Movie&query=Other", 400}, {"unknown", "a", "query=Movie&api_key=secret", 400},
		{"year_empty", "a", "query=Movie&year=", 400}, {"year_zero", "a", "query=Movie&year=0000", 400}, {"year_short", "a", "query=Movie&year=999", 400}, {"year_long", "a", "query=Movie&year=10000", 400}, {"year_sign", "a", "query=Movie&year=%2B2024", 400}, {"year_duplicate", "a", "query=Movie&year=2024&year=2025", 400},
		{"language_empty", "a", "query=Movie&language=", 400}, {"language_unknown", "a", "query=Movie&language=fr-FR", 400}, {"language_duplicate", "a", "query=Movie&language=en-US&language=zh-TW", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &httpMovieProvider{}
			h, _ := metadataFixture(t, p)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, accountRequest("GET", "/api/v1/metadata/tmdb/movies?"+tc.query, "", tc.role))
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if (tc.status == 200 && p.calls != 1) || (tc.status != 200 && p.calls != 0) {
				t.Fatal("unauthorized/invalid search reached provider")
			}
			if tc.status == 200 {
				var body struct {
					Data domain.MovieMatches `json:"data"`
				}
				if json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Data.Query != "Movie" || len(body.Data.Candidates) != 1 || !body.Data.Candidates[0].NeedsConfirmation || !body.Data.Candidates[0].ExactTitle {
					t.Fatal("match contract differs")
				}
				if tc.name == "admin" && (!body.Data.Candidates[0].ExactYear || p.language != "zh-TW") {
					t.Fatal("year/locale ignored")
				}
			}
		})
	}
}

func TestMovieSearchHTTPErrorAndSpecification(t *testing.T) {
	p := &httpMovieProvider{fail: domain.ErrMetadataUnavailable}
	h, cfg := metadataFixture(t, p)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, accountRequest("GET", "/api/v1/metadata/tmdb/movies?query=Movie", "", "a"))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "metadata_unavailable") {
		t.Fatal("provider failure contract differs")
	}
	path := "/api/v1/metadata/tmdb/movies"
	op := Specification(cfg)["paths"].(map[string]any)[path].(map[string]any)["get"].(map[string]any)
	if len(op["security"].([]any)) != 1 || len(op["parameters"].([]any)) != 4 {
		t.Fatal("search specification missing authority/inputs")
	}
	cfg.TMDBAPIKey = ""
	if _, ok := Specification(cfg)["paths"].(map[string]any)[path]; ok {
		t.Fatal("disabled search advertised")
	}
	base := newAccountHTTPFixture(t, httpAccountRepository{}, nil)
	if w := base.serve(accountRequest("GET", path+"?query=Movie", "", "a")); w.Code != 404 {
		t.Fatal("disabled search exposed")
	}
}
