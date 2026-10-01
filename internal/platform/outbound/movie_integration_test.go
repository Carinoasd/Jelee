package outbound_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/adapter/metadata"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/platform/outbound"
)

func TestMovieCandidateThroughActualTLSAndApplicationCache(t *testing.T) {
	cert, roots := providerCertificate(t)
	var movieCalls, authCalls atomic.Int32
	key := strings.Repeat("a", 32)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "api.themoviedb.org" || r.TLS == nil || r.URL.Query().Get("api_key") != key {
			t.Error("guarded movie request differs")
		}
		switch r.URL.Path {
		case "/3/movie/12":
			movieCalls.Add(1)
			if r.URL.Query().Get("language") != "zh-TW" {
				t.Error("locale lost")
			}
			fmt.Fprint(w, `{"id":12,"title":"電影","release_date":"2024-02-29","homepage":"http://127.0.0.1/secret"}`)
		case "/3/authentication":
			authCalls.Add(1)
			fmt.Fprint(w, `{"success":true,"status_code":1}`)
		default:
			t.Error("unexpected upstream endpoint")
			w.WriteHeader(404)
		}
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	defer srv.Close()
	client, err := outbound.NewMappedTestClient(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}, func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "93.184.216.34:443" {
			t.Error("unvalidated dial")
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
		value, err := service.Movie(context.Background(), 12, "zh-TW")
		if err != nil || value.Title != "電影" || value.SourceURL != "https://www.themoviedb.org/movie/12" || value.FetchedAt.IsZero() {
			t.Fatalf("candidate=%+v error=%v", value, err)
		}
		if err := provider.ValidateCredentials(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if movieCalls.Load() != 1 || authCalls.Load() != 2 {
		t.Fatalf("movie=%d authentication=%d", movieCalls.Load(), authCalls.Load())
	}
}
