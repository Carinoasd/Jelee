package postgres

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	httpapi "github.com/MoYuanCN/Jelee/internal/adapter/http"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/config"
	"github.com/MoYuanCN/Jelee/internal/platform/password"
)

type preferenceMovieProvider struct {
	app.MetadataProvider
	calls          int
	language       string
	imageCalls     int
	imageLanguages []string
}

func (p *preferenceMovieProvider) Images(_ context.Context, resource string, id int32, languages []string) (domain.MetadataImages, error) {
	p.imageCalls++
	p.imageLanguages = append([]string(nil), languages...)
	path := "movie"
	if resource == "series" {
		path = "tv"
	}
	return domain.MetadataImages{Resource: resource, ProviderID: id, Source: "TMDB", SourceURL: "https://www.themoviedb.org/" + path + "/" + strconv.Itoa(int(id)), FetchedAt: time.Now(), ImageLanguages: append([]string(nil), languages...), Candidates: []domain.MetadataImageCandidate{{Kind: "poster", Language: languages[0], FilePath: "/poster.jpg", URL: "https://image.tmdb.org/t/p/original/poster.jpg", Width: 500, Height: 750}}}, nil
}

func (p *preferenceMovieProvider) Movie(_ context.Context, id int32, language string) (domain.MovieCandidate, error) {
	p.calls++
	p.language = language
	return domain.MovieCandidate{ProviderID: id, Title: "Title", Overview: "Summary", Language: language, FetchedAt: time.Now()}, nil
}

func TestMetadataPreferencesActualHTTPAndPostgres(t *testing.T) {
	f := newJobFixture(t)
	id := f.registration.Library.ID
	grant := accountLogin(t, f.ctx, f.s, "job-admin")
	cfg, err := config.LoadWith(func(key string) (string, bool) {
		if key == "JELEE_DATABASE_URL" {
			return f.s.Pool.Config().ConnString(), true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg.DatabaseURL = f.s.Pool.Config().ConnString()
	cfg.EnableAccounts = true
	cfg.TMDBAPIKey = strings.Repeat("a", 32)
	hasher, err := password.New(password.Config{MemoryKiB: uint32(cfg.Accounts.PasswordMemoryKiB), Iterations: uint32(cfg.Accounts.PasswordIterations), Parallelism: uint8(cfg.Accounts.PasswordParallelism), MaxConcurrent: cfg.Accounts.PasswordConcurrency})
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := app.NewAccounts(f.s, hasher, app.AccountOptions{SessionTTL: time.Hour, MaxSessions: 8, LockAfter: 5, LockFor: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	provider := &preferenceMovieProvider{}
	metadata, err := app.NewMetadata(provider)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err = metadata.WithLibraryPreferences(f.s)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := httpapi.NewWithJobs(cfg, f.s, app.NewCatalog(f.s), f.s, slog.New(slog.NewTextHandler(io.Discard, nil)), accounts, nil, metadata)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+grant.Token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	path := "/api/v1/libraries/" + id + "/metadata-preferences"
	w := request("PUT", path, `{"language":"ja-JP","expectedRevision":1,"imageLanguages":["ja","null"]}`)
	if w.Code != 200 {
		t.Fatalf("update=%d %s", w.Code, w.Body.String())
	}
	w = request("GET", path, "")
	var body struct {
		Data domain.MetadataPreferences `json:"data"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Data.Language != "ja-JP" || body.Data.Revision != 2 {
		t.Fatalf("read=%d %s", w.Code, w.Body.String())
	}
	query := "/api/v1/metadata/tmdb/movies/12?libraryId=" + id
	w = request("GET", query, "")
	if w.Code != 200 || provider.language != "ja-JP" || provider.calls != 1 {
		t.Fatalf("library query=%d %s", w.Code, w.Body.String())
	}
	w = request("GET", query+"&language=en-US", "")
	if w.Code != 200 || provider.language != "en-US" || provider.calls != 2 {
		t.Fatal("explicit override lost")
	}
	w = request("PUT", path, `{"language":"en-US","expectedRevision":1}`)
	if w.Code != 409 {
		t.Fatal("HTTP stale revision overwrote preference")
	}
	imageQuery := "/api/v1/metadata/tmdb/movies/12/images?libraryId=" + id
	w = request("GET", imageQuery, "")
	var images struct {
		Data domain.MetadataImages `json:"data"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &images) != nil || !reflect.DeepEqual(provider.imageLanguages, []string{"ja", "null"}) || len(images.Data.Candidates) != 1 || !images.Data.Candidates[0].NeedsConfirmation {
		t.Fatalf("image preference not wired: status=%d body=%s", w.Code, w.Body.String())
	}
	w = request("PUT", path, `{"language":"en-US","expectedRevision":2}`)
	if w.Code != 200 {
		t.Fatal("legacy update failed")
	}
	w = request("GET", imageQuery, "")
	if w.Code != 200 || !reflect.DeepEqual(provider.imageLanguages, []string{"ja", "null"}) {
		t.Fatal("legacy update lost image preference")
	}
	if _, err = f.s.Pool.Exec(f.ctx, `UPDATE sessions SET revoked_at=now() WHERE id=$1::uuid`, grant.Session.ID); err != nil {
		t.Fatal(err)
	}
	w = request("GET", query, "")
	if w.Code != 401 || provider.calls != 2 {
		t.Fatal("revoked session reached metadata provider")
	}
	w = request("GET", imageQuery, "")
	if w.Code != 401 || provider.imageCalls != 2 {
		t.Fatal("revoked session reached image provider")
	}
}
