package outbound_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/metadata"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/outbound"
)

func TestMetadataLanguageFallbackThroughActualTLS(t *testing.T) {
	cert, roots := providerCertificate(t)
	key := strings.Repeat("a", 32)
	var mu sync.Mutex
	var details, searches []string
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "api.themoviedb.org" || r.TLS == nil || r.URL.Query().Get("api_key") != key {
			t.Error("controlled request differs")
		}
		language := r.URL.Query().Get("language")
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/3/movie/12":
			details = append(details, language)
			overview := ""
			if language == "en-US" {
				overview = "English overview"
			}
			fmt.Fprintf(w, `{"id":12,"title":%q,"overview":%q}`, language, overview)
		case "/3/search/movie":
			searches = append(searches, language)
			if language == "ja-JP" {
				fmt.Fprint(w, `{"page":1,"results":[{"id":12,"title":"Title","overview":"Japanese overview"}]}`)
			} else {
				fmt.Fprint(w, `{"page":1,"results":[]}`)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	defer srv.Close()
	client, err := outbound.NewMappedTestClient(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}, func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "93.184.216.34:443" {
			t.Error("unpinned dial")
		}
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}, roots)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := metadata.NewTMDBWithClient(key, client)
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	service, err := app.NewMetadata(provider)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		v, err := service.Movie(context.Background(), 12, "zh-CN")
		if err != nil || v.Title != "zh-CN" || v.Language != "zh-CN" || v.Overview != "English overview" || v.OverviewSource.RequestedLanguage != "en-US" || v.OverviewSource.FetchedAt.IsZero() || v.OverviewSource.FetchedAt.Before(v.FetchedAt) {
			t.Fatalf("fallback=%+v error=%v", v, err)
		}
	}
	// The per-language provider cache stores original responses, never merged application views.
	raw, err := provider.Movie(context.Background(), 12, "zh-CN")
	if err != nil || raw.Overview != "" || raw.OverviewSource.RequestedLanguage != "" {
		t.Fatal("fallback poisoned source cache")
	}
	result, err := service.SearchMovies(context.Background(), domain.MovieSearchInput{Query: "Title", Language: "zh-TW"})
	if err != nil || len(result.Candidates) != 1 || result.Language != "zh-TW" {
		t.Fatalf("search error=%v", err)
	}
	match := result.Candidates[0]
	if !match.NeedsConfirmation || match.Movie.Language != "ja-JP" || match.Movie.OverviewSource.RequestedLanguage != "ja-JP" || match.Movie.OverviewSource.FetchedAt.IsZero() {
		t.Fatal("search provenance or confirmation lost")
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(details, []string{"zh-CN", "zh-TW", "ja-JP", "en-US"}) || !reflect.DeepEqual(searches, []string{"zh-TW", "ja-JP"}) {
		t.Fatalf("details=%v searches=%v", details, searches)
	}
}
