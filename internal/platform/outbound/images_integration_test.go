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
	"sync/atomic"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/metadata"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/platform/outbound"
)

func TestImagePreferencesThroughActualTLSAndApplication(t *testing.T) {
	cert, roots := providerCertificate(t)
	key := strings.Repeat("a", 32)
	var movies, series atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "api.themoviedb.org" || r.TLS == nil || r.URL.Query().Get("api_key") != key || r.URL.Query().Get("include_image_language") != "en,null" || r.URL.Query().Get("language") != "" {
			t.Error("controlled image request differs")
		}
		switch r.URL.Path {
		case "/3/movie/12/images":
			movies.Add(1)
		case "/3/tv/12/images":
			series.Add(1)
		default:
			w.WriteHeader(404)
			return
		}
		fmt.Fprint(w, `{"id":12,"posters":[{"iso_639_1":"zh","file_path":"/zh.jpg","width":500,"height":750,"vote_average":10,"vote_count":50},{"iso_639_1":null,"file_path":"/none.jpg","width":500,"height":750,"vote_average":9,"vote_count":5},{"iso_639_1":"en","file_path":"/english.jpg","width":500,"height":750,"vote_average":7,"vote_count":2}],"backdrops":[]}`)
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	defer srv.Close()
	client, err := outbound.NewMappedTestClient(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}, func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "93.184.216.34:443" {
			t.Error("unpinned image dial")
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
		v, err := service.Images(context.Background(), "movie", 12, []string{"en", "null"})
		if err != nil || len(v.Candidates) != 2 || v.Candidates[0].Language != "en" || v.Candidates[1].Language != "null" || v.SourceURL != "https://www.themoviedb.org/movie/12" || v.FetchedAt.IsZero() || !reflect.DeepEqual(v.ImageLanguages, []string{"en", "null"}) {
			t.Fatalf("images=%+v error=%v", v, err)
		}
		for _, image := range v.Candidates {
			if !image.NeedsConfirmation || !strings.HasPrefix(image.URL, "https://image.tmdb.org/t/p/original/") {
				t.Fatal("unsafe image or automatic confirmation")
			}
		}
		v.Candidates[0].FilePath = "changed"
		v.ImageLanguages[0] = "zh"
	}
	raw, err := provider.Images(context.Background(), "movie", 12, []string{"en", "null"})
	if err != nil || len(raw.Candidates) != 3 || raw.Candidates[0].Language != "zh" || raw.Candidates[0].NeedsConfirmation || raw.Candidates[0].FilePath != "/zh.jpg" {
		t.Fatal("application mutated provider cache")
	}
	v, err := service.Images(context.Background(), "series", 12, []string{"en", "null"})
	if err != nil || v.SourceURL != "https://www.themoviedb.org/tv/12" || movies.Load() != 1 || series.Load() != 1 {
		t.Fatal("image source cache collision")
	}
}
