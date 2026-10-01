package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetadataRoutesUseTrustedProfileLocale(t *testing.T) {
	paths := []string{
		"/api/v1/metadata/tmdb/movies/12",
		"/api/v1/metadata/tmdb/movies?query=Title",
		"/api/v1/metadata/tmdb/series/12",
		"/api/v1/metadata/tmdb/series?query=Title",
		"/api/v1/metadata/tmdb/series/12/seasons/0",
		"/api/v1/metadata/tmdb/series/12/seasons/0/episodes/1",
	}
	for _, path := range paths {
		for _, tc := range []struct {
			name, locale, query, want string
			status                    int
		}{
			{"profile", "ja-JP", "", "ja-JP", 200},
			{"override", "ja-JP", "language=zh-TW", "zh-TW", 200},
			{"missing_profile", "", "", "zh-CN", 200},
			{"unknown_profile", "fr-FR", "", "zh-CN", 200},
			{"empty_override", "ja-JP", "language=", "", 400},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				p := &httpMovieProvider{}
				h, _ := metadataFixture(t, p, tc.locale)
				target := path
				if tc.query != "" {
					separator := "?"
					if strings.Contains(target, "?") {
						separator = "&"
					}
					target += separator + tc.query
				}
				r := accountRequest("GET", target, "", "a")
				r.Header.Set("Accept-Language", "en-US")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != tc.status || (tc.status == 200 && (p.calls != 1 || p.language != tc.want)) || (tc.status != 200 && p.calls != 0) {
					t.Fatalf("status=%d calls=%d language=%s body=%s", w.Code, p.calls, p.language, w.Body.String())
				}
			})
		}
	}
}
