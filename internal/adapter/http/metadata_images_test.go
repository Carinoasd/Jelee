package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestMetadataImageHTTPAuthorityPreferenceAndContract(t *testing.T) {
	for _, resource := range []string{"movies", "series"} {
		for _, tc := range []struct {
			name, path, role string
			status           int
		}{
			{"admin", "12", "a", 200}, {"anonymous", "12", "", 401}, {"user", "12", "u", 403},
			{"noncanonical", "012", "a", 400}, {"zero", "0", "a", 400}, {"overflow", "2147483648", "a", 400},
			{"library", "12?libraryId=" + libraryID, "a", 200}, {"empty_library", "12?libraryId=", "a", 400},
			{"unknown_library", "12?libraryId=00000000-0000-4000-8000-000000000001", "a", 404},
			{"unknown_query", "12?language=en-US", "a", 400}, {"duplicate", "12?libraryId=" + libraryID + "&libraryId=" + libraryID, "a", 400},
		} {
			t.Run(resource+"/"+tc.name, func(t *testing.T) {
				prefs := &httpMetadataPreferences{value: domain.MetadataPreferences{LibraryID: libraryID, Language: "zh-CN", Revision: 1, ImageLanguages: []string{"ja", "null"}}}
				provider := &httpMovieProvider{preferences: prefs}
				h, cfg := metadataFixture(t, provider, "en-US")
				path, query, hasQuery := strings.Cut(tc.path, "?")
				target := "/api/v1/metadata/tmdb/" + resource + "/" + path + "/images"
				if hasQuery {
					target += "?" + query
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, accountRequest("GET", target, "", tc.role))
				if w.Code != tc.status || (tc.status == 200 && provider.calls != 1) || (tc.status != 200 && provider.calls != 0) {
					t.Fatalf("status=%d calls=%d body=%s", w.Code, provider.calls, w.Body.String())
				}
				if tc.status == 200 {
					want := []string{"en", "null"}
					if tc.name == "library" {
						want = []string{"ja", "null"}
					}
					if !reflect.DeepEqual(provider.imageLanguages, want) {
						t.Fatal("image language preference lost")
					}
					var body struct {
						Data domain.MetadataImages `json:"data"`
					}
					if json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Data.Candidates) != 1 || !body.Data.Candidates[0].NeedsConfirmation {
						t.Fatal("image confirmation envelope differs")
					}
				}
				if _, ok := Specification(cfg)["paths"].(map[string]any)["/api/v1/metadata/tmdb/"+resource+"/{id}/images"]; !ok {
					t.Fatal("image route not advertised")
				}
			})
		}
	}
}

func TestMetadataImagePreferenceStrictUpdatesAndLegacyPreservation(t *testing.T) {
	prefs := &httpMetadataPreferences{value: domain.MetadataPreferences{LibraryID: libraryID, Language: "en-US", Revision: 1, ImageLanguages: []string{"ja", "null"}}}
	h, _ := metadataFixture(t, &httpMovieProvider{preferences: prefs})
	path := "/api/v1/libraries/" + libraryID + "/metadata-preferences"
	for _, raw := range []string{`null`, `[]`, `["en","en"]`, `[null,"en"]`, `["zh-TW"]`, `["en", "ja", "zh", "null", "en"]`} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, accountRequest("PUT", path, `{"language":"en-US","expectedRevision":1,"imageLanguages":`+raw+`}`, "a"))
		if w.Code != 400 || prefs.value.Revision != 1 {
			t.Fatalf("invalid image preference accepted %s status=%d", raw, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, accountRequest("PUT", path, `{"language":"ja-JP","expectedRevision":1}`, "a"))
	if w.Code != 200 || !reflect.DeepEqual(prefs.value.ImageLanguages, []string{"ja", "null"}) {
		t.Fatal("legacy update discarded image preference")
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, accountRequest("PUT", path, `{"language":"ja-JP","expectedRevision":2,"imageLanguages":["en","null"]}`, "a"))
	if w.Code != 200 || !reflect.DeepEqual(prefs.value.ImageLanguages, []string{"en", "null"}) || prefs.value.Revision != 3 {
		t.Fatal("ordered image preference not updated")
	}
}
