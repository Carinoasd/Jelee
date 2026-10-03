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
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/outbound"
)

func TestSeriesTwentyCandidatesThroughActualTLSAndIsolatedCache(t *testing.T) {
	cert, roots := providerCertificate(t)
	key := strings.Repeat("a", 32)
	var searches, seriesCalls, movieCalls atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || r.Host != "api.themoviedb.org" || r.URL.Query().Get("api_key") != key {
			t.Error("controlled request differs")
		}
		switch r.URL.Path {
		case "/3/search/tv":
			searches.Add(1)
			q := r.URL.Query()
			if q.Get("first_air_date_year") != "2024" || q.Get("year") != "" || q.Get("query") != "Series & year=attacker" || q.Get("language") != "en-US" || q.Get("include_adult") != "false" || q.Get("page") != "1" {
				t.Error("first-air-date/query contract differs")
			}
			fmt.Fprint(w, `{"page":1,"results":[`)
			for i := 0; i < 20; i++ {
				if i != 0 {
					fmt.Fprint(w, ",")
				}
				fmt.Fprintf(w, `{"id":%d,"name":"Series & year=attacker","first_air_date":"2024-01-01"}`, 12+i)
			}
			fmt.Fprint(w, `]}`)
		case "/3/tv/12":
			seriesCalls.Add(1)
			fmt.Fprint(w, `{"id":12,"name":"Series & year=attacker","first_air_date":"2024-01-01"}`)
		case "/3/movie/12":
			movieCalls.Add(1)
			fmt.Fprint(w, `{"id":12,"title":"Movie","release_date":"1990-01-01"}`)
		default:
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
			t.Error("IP pin lost")
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
	result, err := service.SearchSeries(context.Background(), domain.SeriesSearchInput{Query: "Series & year=attacker", Year: 2024, Language: "en-US"})
	if err != nil || len(result.Candidates) != 20 {
		t.Fatalf("series search error=%v", err)
	}
	for _, match := range result.Candidates {
		if !match.ExactTitle || !match.ExactYear || !match.NeedsConfirmation {
			t.Fatal("series confirmation differs")
		}
	}
	if seriesCalls.Load() != 0 || movieCalls.Load() != 0 {
		t.Fatal("search fetched selected details")
	}
	for i := 0; i < 2; i++ {
		value, err := service.Series(context.Background(), 12, "en-US")
		if err != nil || value.SourceURL != "https://www.themoviedb.org/tv/12" || value.FirstAirDate != "2024-01-01" || value.FetchedAt.IsZero() {
			t.Fatal("series details differ")
		}
	}
	movie, err := service.Movie(context.Background(), 12, "en-US")
	if err != nil || movie.Title != "Movie" {
		t.Fatal("series polluted movie cache")
	}
	if searches.Load() != 1 || seriesCalls.Load() != 1 || movieCalls.Load() != 1 {
		t.Fatalf("search=%d series=%d movie=%d", searches.Load(), seriesCalls.Load(), movieCalls.Load())
	}
}
