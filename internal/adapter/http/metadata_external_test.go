package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/domain"
)

// httpExternalProvider adds the external ID and season/episode image
// capabilities to the shared HTTP metadata fixture.
type httpExternalProvider struct {
	*httpMovieProvider
	source, id string
	target     [3]int32
}

func (p *httpExternalProvider) FindByExternalID(ctx context.Context, source, id, language string) (domain.ExternalIDMatches, error) {
	p.calls++
	p.language, p.source, p.id = language, source, id
	if err := ctx.Err(); err != nil {
		return domain.ExternalIDMatches{}, err
	}
	at := time.Now().UTC()
	return domain.ExternalIDMatches{ExternalSource: source, ExternalID: id, Language: language, FetchedAt: at,
		Movies: []domain.MovieCandidate{{ProviderID: 550, Title: "電影", Overview: "Summary", Language: language, Source: "TMDB", SourceURL: "https://www.themoviedb.org/movie/550", FetchedAt: at, ReleaseDate: "1999-10-15"}},
		Series: []domain.SeriesCandidate{}}, p.fail
}

func (p *httpExternalProvider) EpisodeImages(ctx context.Context, series, season, episode int32, languages []string) (domain.MetadataImages, error) {
	p.calls++
	p.imageLanguages = append([]string(nil), languages...)
	p.target = [3]int32{series, season, episode}
	if err := ctx.Err(); err != nil {
		return domain.MetadataImages{}, err
	}
	kind, resource, path := "poster", "season", "https://www.themoviedb.org/tv/"+strconv.Itoa(int(series))+"/season/"+strconv.Itoa(int(season))
	if episode > 0 {
		kind, resource, path = "still", "episode", path+"/episode/"+strconv.Itoa(int(episode))
	}
	number := season
	return domain.MetadataImages{Resource: resource, ProviderID: 4000, SeriesID: series, SeasonNumber: &number, EpisodeNumber: episode, Source: "TMDB", SourceURL: path, FetchedAt: time.Now(), ImageLanguages: append([]string(nil), languages...), Candidates: []domain.MetadataImageCandidate{{Kind: kind, Language: languages[0], FilePath: "/image.jpg", URL: "https://image.tmdb.org/t/p/original/image.jpg", Width: 500, Height: 750}}}, p.fail
}

func TestExternalIDFindHTTPAuthorityLanguageAndContract(t *testing.T) {
	for _, tc := range []struct {
		name, query, role, language string
		status                      int
	}{
		{"admin_imdb", "source=imdb_id&id=tt0137523", "a", "en-US", 200},
		{"tvdb", "source=tvdb_id&id=81189", "a", "en-US", 200},
		{"wikidata_override", "source=wikidata_id&id=Q190050&language=ja-JP", "a", "ja-JP", 200},
		{"library_language", "source=imdb_id&id=tt0137523&libraryId=" + libraryID, "a", "zh-TW", 200},
		{"override_beats_library", "source=imdb_id&id=tt0137523&libraryId=" + libraryID + "&language=en-US", "a", "en-US", 200},
		{"anonymous", "source=imdb_id&id=tt0137523", "", "", 401},
		{"user", "source=imdb_id&id=tt0137523", "u", "", 403},
		{"missing_source", "id=tt0137523", "a", "", 400},
		{"missing_id", "source=imdb_id", "a", "", 400},
		{"tmdb_source", "source=tmdb_id&id=550", "a", "", 400},
		{"person_id", "source=imdb_id&id=nm0000093", "a", "", 400},
		{"tvdb_zero", "source=tvdb_id&id=0", "a", "", 400},
		{"tvdb_overflow", "source=tvdb_id&id=2147483648", "a", "", 400},
		{"path_escape", "source=imdb_id&id=tt0137523%2F..", "a", "", 400},
		{"bad_language", "source=imdb_id&id=tt0137523&language=fr-FR", "a", "", 400},
		{"unknown_query", "source=imdb_id&id=tt0137523&type=movie", "a", "", 400},
		{"duplicate", "source=imdb_id&source=imdb_id&id=tt0137523", "a", "", 400},
		{"unknown_library", "source=imdb_id&id=tt0137523&libraryId=00000000-0000-4000-8000-000000000001", "a", "", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prefs := &httpMetadataPreferences{value: domain.MetadataPreferences{LibraryID: libraryID, Language: "zh-TW", Revision: 1, ImageLanguages: []string{"ja", "null"}}}
			provider := &httpExternalProvider{httpMovieProvider: &httpMovieProvider{preferences: prefs}}
			h, cfg := metadataProviderFixture(t, provider, prefs, "en-US")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, accountRequest("GET", "/api/v1/metadata/tmdb/find?"+tc.query, "", tc.role))
			if w.Code != tc.status || (tc.status == 200) != (provider.calls == 1) || provider.calls > 1 {
				t.Fatalf("status=%d calls=%d body=%s", w.Code, provider.calls, w.Body.String())
			}
			if tc.status != 200 {
				return
			}
			if provider.language != tc.language {
				t.Fatalf("language=%s", provider.language)
			}
			var body struct {
				Data domain.ExternalIDMatches `json:"data"`
			}
			if json.Unmarshal(w.Body.Bytes(), &body) != nil || !body.Data.NeedsConfirmation || len(body.Data.Movies) != 1 || body.Data.Movies[0].OverviewSource.RequestedLanguage != tc.language || body.Data.ExternalSource != provider.source || body.Data.ExternalID != provider.id {
				t.Fatalf("find envelope differs: %s", w.Body.String())
			}
			if _, ok := Specification(cfg)["paths"].(map[string]any)["/api/v1/metadata/tmdb/find"]; !ok {
				t.Fatal("find route not advertised")
			}
		})
	}
	provider := &httpExternalProvider{httpMovieProvider: &httpMovieProvider{fail: domain.ErrMetadataUnavailable}}
	h, _ := metadataProviderFixture(t, provider, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, accountRequest("GET", "/api/v1/metadata/tmdb/find?source=imdb_id&id=tt0137523", "", "a"))
	if w.Code != 503 {
		t.Fatalf("provider failure status=%d", w.Code)
	}
}

func TestSeasonEpisodeImageHTTPAuthorityPreferenceAndContract(t *testing.T) {
	for _, tc := range []struct {
		name, path, role string
		status           int
		target           [3]int32
	}{
		{"season", "12/seasons/1/images", "a", 200, [3]int32{12, 1, 0}},
		{"specials", "12/seasons/0/images", "a", 200, [3]int32{12, 0, 0}},
		{"episode", "12/seasons/1/episodes/2/images", "a", 200, [3]int32{12, 1, 2}},
		{"library", "12/seasons/1/episodes/2/images?libraryId=" + libraryID, "a", 200, [3]int32{12, 1, 2}},
		{"anonymous", "12/seasons/1/images", "", 401, [3]int32{}},
		{"user", "12/seasons/1/episodes/2/images", "u", 403, [3]int32{}},
		{"zero_series", "0/seasons/1/images", "a", 400, [3]int32{}},
		{"negative_season", "12/seasons/-1/images", "a", 400, [3]int32{}},
		{"zero_episode", "12/seasons/1/episodes/0/images", "a", 400, [3]int32{}},
		{"noncanonical", "12/seasons/01/images", "a", 400, [3]int32{}},
		{"overflow", "12/seasons/1/episodes/2147483648/images", "a", 400, [3]int32{}},
		{"language_query", "12/seasons/1/images?language=en-US", "a", 400, [3]int32{}},
		{"unknown_library", "12/seasons/1/images?libraryId=00000000-0000-4000-8000-000000000001", "a", 404, [3]int32{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prefs := &httpMetadataPreferences{value: domain.MetadataPreferences{LibraryID: libraryID, Language: "zh-CN", Revision: 1, ImageLanguages: []string{"ja", "null"}}}
			provider := &httpExternalProvider{httpMovieProvider: &httpMovieProvider{preferences: prefs}}
			h, cfg := metadataProviderFixture(t, provider, prefs, "en-US")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, accountRequest("GET", "/api/v1/metadata/tmdb/series/"+tc.path, "", tc.role))
			if w.Code != tc.status || (tc.status == 200) != (provider.calls == 1) || provider.calls > 1 {
				t.Fatalf("status=%d calls=%d body=%s", w.Code, provider.calls, w.Body.String())
			}
			if tc.status != 200 {
				return
			}
			want := []string{"en", "null"}
			if tc.name == "library" {
				want = []string{"ja", "null"}
			}
			if provider.target != tc.target || !reflect.DeepEqual(provider.imageLanguages, want) {
				t.Fatalf("target=%v languages=%v", provider.target, provider.imageLanguages)
			}
			var body struct {
				Data domain.MetadataImages `json:"data"`
			}
			if json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Data.Candidates) != 1 || !body.Data.Candidates[0].NeedsConfirmation || body.Data.SeriesID != 12 || body.Data.SeasonNumber == nil || *body.Data.SeasonNumber != tc.target[1] || body.Data.EpisodeNumber != tc.target[2] {
				t.Fatalf("image envelope differs: %s", w.Body.String())
			}
			paths := Specification(cfg)["paths"].(map[string]any)
			for _, path := range []string{"/api/v1/metadata/tmdb/series/{id}/seasons/{season}/images", "/api/v1/metadata/tmdb/series/{id}/seasons/{season}/episodes/{episode}/images"} {
				if _, ok := paths[path]; !ok {
					t.Fatal("season or episode image route not advertised")
				}
			}
		})
	}
}
