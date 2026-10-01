package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

type httpMetadataPreferences struct {
	value domain.MetadataPreferences
	calls int
	fail  error
}

func (p *httpMetadataPreferences) MetadataPreferences(_ context.Context, a domain.Actor, id string) (domain.MetadataPreferences, error) {
	p.calls++
	if a.UserID != userID || a.SessionID != sessionID {
		return domain.MetadataPreferences{}, domain.ErrUnauthenticated
	}
	if p.fail != nil {
		return domain.MetadataPreferences{}, p.fail
	}
	if id != libraryID {
		return domain.MetadataPreferences{}, domain.ErrNotFound
	}
	return p.value, nil
}
func (p *httpMetadataPreferences) UpdateMetadataPreferences(ctx context.Context, a domain.Actor, id, language string, expected int64) (domain.MetadataPreferences, error) {
	v, err := p.MetadataPreferences(ctx, a, id)
	if err != nil {
		return v, err
	}
	if v.Revision != expected {
		return domain.MetadataPreferences{}, domain.ErrConflict
	}
	p.value.Language = language
	p.value.Revision++
	return p.value, nil
}

func TestMetadataLibraryPreferenceRoutesAndQuerySelection(t *testing.T) {
	paths := []string{"/api/v1/metadata/tmdb/movies/12", "/api/v1/metadata/tmdb/movies?query=Title", "/api/v1/metadata/tmdb/series/12", "/api/v1/metadata/tmdb/series?query=Title", "/api/v1/metadata/tmdb/series/12/seasons/0", "/api/v1/metadata/tmdb/series/12/seasons/0/episodes/1"}
	for _, path := range paths {
		for _, override := range []bool{false, true} {
			t.Run(path+"/"+map[bool]string{false: "library", true: "override"}[override], func(t *testing.T) {
				prefs := &httpMetadataPreferences{value: domain.MetadataPreferences{LibraryID: libraryID, Language: "en-US", Revision: 1}}
				provider := &httpMovieProvider{preferences: prefs}
				h, _ := metadataFixture(t, provider, "ja-JP")
				separator := "?"
				if strings.Contains(path, "?") {
					separator = "&"
				}
				target := path + separator + "libraryId=" + libraryID
				want := "en-US"
				if override {
					target += "&language=zh-TW"
					want = "zh-TW"
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, accountRequest("GET", target, "", "a"))
				if w.Code != 200 || provider.language != want || provider.calls != 1 || prefs.calls != 1 {
					t.Fatalf("status=%d language=%s prefs=%d", w.Code, provider.language, prefs.calls)
				}
				prefs.fail = domain.ErrUnauthenticated
				provider.calls = 0
				w = httptest.NewRecorder()
				h.ServeHTTP(w, accountRequest("GET", target, "", "a"))
				if w.Code != 401 || provider.calls != 0 {
					t.Fatal("revoked library access reached provider")
				}
			})
		}
	}
	prefs := &httpMetadataPreferences{value: domain.MetadataPreferences{LibraryID: libraryID, Language: "zh-CN", Revision: 1}}
	provider := &httpMovieProvider{preferences: prefs}
	h, cfg := metadataFixture(t, provider)
	path := "/api/v1/libraries/" + libraryID + "/metadata-preferences"
	for _, tc := range []struct {
		method, role, body string
		status             int
	}{
		{"GET", "", "", 401}, {"GET", "u", "", 403}, {"PUT", "u", `{"language":"en-US","expectedRevision":1}`, 403},
		{"GET", "a", "", 200}, {"PUT", "a", `{"language":"ja-JP","expectedRevision":1}`, 200},
		{"PUT", "a", `{"language":"en-US","expectedRevision":1}`, 409},
		{"PUT", "a", `{"language":"fr-FR","expectedRevision":2}`, 400},
		{"PUT", "a", `{"language":"en-US"}`, 400},
		{"PUT", "a", `{"language":"en-US","expectedRevision":2,"extra":true}`, 400},
		{"PUT", "a", `{"language":"en-US","language":"ja-JP","expectedRevision":2}`, 400},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, accountRequest(tc.method, path, tc.body, tc.role))
		if w.Code != tc.status {
			t.Fatalf("%s status=%d body=%s", tc.method, w.Code, w.Body.String())
		}
		if tc.method == "GET" && tc.status == 200 {
			var body struct {
				Data domain.MetadataPreferences `json:"data"`
			}
			if json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Data.LibraryID != libraryID {
				t.Fatal("preference envelope differs")
			}
		}
	}
	if prefs.value.Revision != 2 || prefs.value.Language != "ja-JP" {
		t.Fatal("rejected update changed preferences")
	}
	if _, ok := Specification(cfg)["paths"].(map[string]any)["/api/v1/libraries/{id}/metadata-preferences"]; !ok {
		t.Fatal("preferences not advertised")
	}
	cfg.TMDBAPIKey = ""
	if _, ok := Specification(cfg)["paths"].(map[string]any)["/api/v1/libraries/{id}/metadata-preferences"]; ok {
		t.Fatal("disabled preferences advertised")
	}
}
