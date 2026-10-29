package outbound_test

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	httpapi "github.com/MoYuanCN/Jelee/internal/adapter/http"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
)

// Find and season/episode images read the stored library preferences through
// the production HTTP routes, the governed TLS adapter and PostgreSQL.
func TestFindAndEpisodeImagesThroughTLSHTTPAndPostgres(t *testing.T) {
	ctx, store, grant, library := metadataApplyStore(t)
	key := strings.Repeat("a", 32)
	var finds, images atomic.Int32
	var findLanguage, imageLanguages atomic.Value
	provider := mappedTMDB(t, key, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Host != "api.themoviedb.org" || r.TLS == nil || q.Get("api_key") != key {
			t.Error("governed provider request differs")
		}
		switch r.URL.Path {
		case "/3/find/tt0137523":
			finds.Add(1)
			findLanguage.Store(q.Get("language"))
			_, _ = fmt.Fprint(w, `{"movie_results":[{"id":550,"title":"ファイト・クラブ","original_title":"Fight Club","overview":"概要","release_date":"1999-10-15"}],"tv_results":[]}`)
		case "/3/tv/1399/season/1/episode/2/images":
			images.Add(1)
			imageLanguages.Store(q.Get("include_image_language"))
			_, _ = fmt.Fprint(w, `{"id":63056,"stills":[{"iso_639_1":"en","file_path":"/en.jpg","width":1280,"height":720,"vote_average":9,"vote_count":9},{"iso_639_1":"ja","file_path":"/ja.jpg","width":1280,"height":720,"vote_average":1,"vote_count":1}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	service, err := app.NewMetadata(provider)
	if err != nil {
		t.Fatal(err)
	}
	if service, err = service.WithLibraryPreferences(store); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadWith(func(name string) (string, bool) {
		if name == "JELEE_DATABASE_URL" {
			return store.Pool.Config().ConnString(), true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg.EnableAccounts, cfg.EnableCatalog, cfg.TMDBAPIKey = true, true, key
	hasher, err := password.New(password.Config{MemoryKiB: uint32(cfg.Accounts.PasswordMemoryKiB), Iterations: uint32(cfg.Accounts.PasswordIterations), Parallelism: uint8(cfg.Accounts.PasswordParallelism), MaxConcurrent: cfg.Accounts.PasswordConcurrency})
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := app.NewAccounts(store, hasher, app.AccountOptions{SessionTTL: time.Hour, MaxSessions: 8, LockAfter: 5, LockFor: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := httpapi.NewWithJobs(cfg, store, app.NewCatalog(store), store, slog.New(slog.NewTextHandler(io.Discard, nil)), accounts, nil, service)
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(handler)
	t.Cleanup(api.Close)
	do := func(method, path, body, token string) (int, []byte) {
		t.Helper()
		r, err := http.NewRequestWithContext(ctx, method, api.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := api.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, data
	}
	libraryID := library.Library.ID
	if status, body := do("PUT", "/api/v1/libraries/"+libraryID+"/metadata-preferences", `{"language":"ja-JP","expectedRevision":1,"imageLanguages":["ja","null"]}`, grant.Token); status != 200 {
		t.Fatalf("preference update status=%d body=%s", status, body)
	}
	status, body := do("GET", "/api/v1/metadata/tmdb/find?source=imdb_id&id=tt0137523&libraryId="+libraryID, "", grant.Token)
	var found struct {
		Data domain.ExternalIDMatches `json:"data"`
	}
	if status != 200 || json.Unmarshal(body, &found) != nil || !found.Data.NeedsConfirmation || found.Data.Language != "ja-JP" || len(found.Data.Movies) != 1 || found.Data.Movies[0].ProviderID != 550 || findLanguage.Load() != "ja-JP" {
		t.Fatalf("find status=%d body=%s", status, body)
	}
	status, body = do("GET", "/api/v1/metadata/tmdb/series/1399/seasons/1/episodes/2/images?libraryId="+libraryID, "", grant.Token)
	var stills struct {
		Data domain.MetadataImages `json:"data"`
	}
	if status != 200 || json.Unmarshal(body, &stills) != nil || imageLanguages.Load() != "ja,null" || len(stills.Data.Candidates) != 1 || stills.Data.Candidates[0].FilePath != "/ja.jpg" || !stills.Data.Candidates[0].NeedsConfirmation || stills.Data.Resource != "episode" {
		t.Fatalf("episode images status=%d body=%s", status, body)
	}
	// Anonymous and unknown-library requests stop before the provider.
	before := finds.Load() + images.Load()
	if status, _ := do("GET", "/api/v1/metadata/tmdb/find?source=imdb_id&id=tt0137523", "", ""); status != 401 {
		t.Fatalf("anonymous find status=%d", status)
	}
	if status, _ := do("GET", "/api/v1/metadata/tmdb/series/1399/seasons/1/images?libraryId=00000000-0000-4000-8000-000000000001", "", grant.Token); status != 404 {
		t.Fatalf("unknown library status=%d", status)
	}
	if finds.Load()+images.Load() != before {
		t.Fatal("rejected request reached the provider")
	}
}
