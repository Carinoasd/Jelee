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

func TestSeasonEpisodeThroughActualTLSCloneAndTupleValidation(t *testing.T) {
	cert, roots := providerCertificate(t)
	key := strings.Repeat("a", 32)
	var seasonCalls, episodeCalls atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || r.Host != "api.themoviedb.org" || r.URL.Query().Get("api_key") != key || r.URL.Query().Get("language") != "zh-TW" {
			t.Error("controlled season request differs")
		}
		switch r.URL.Path {
		case "/3/tv/12/season/0":
			seasonCalls.Add(1)
			fmt.Fprint(w, `{"id":500,"season_number":0,"name":"Specials","overview":"Summary","episodes":[{"id":900,"season_number":0,"episode_number":1,"name":"Special","overview":"Summary","air_date":"2024-01-01"}]}`)
		case "/3/tv/12/season/0/episode/1":
			episodeCalls.Add(1)
			fmt.Fprint(w, `{"id":900,"show_id":12,"season_number":0,"episode_number":1,"name":"Special","overview":"Summary","air_date":"2024-01-01"}`)
		case "/3/tv/12/season/0/episode/2":
			episodeCalls.Add(1)
			fmt.Fprint(w, `{"id":901,"season_number":0,"episode_number":3,"name":"Wrong tuple"}`)
		default:
			t.Error("unexpected season route")
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
	first, err := service.Season(context.Background(), 12, 0, "zh-TW")
	if err != nil || len(first.Episodes) != 1 || first.SourceURL != "https://www.themoviedb.org/tv/12/season/0" || first.FetchedAt.IsZero() {
		t.Fatal("season result differs")
	}
	first.Episodes[0].Title = "mutated initial result"
	second, err := service.Season(context.Background(), 12, 0, "zh-TW")
	if err != nil || second.Episodes[0].Title != "Special" {
		t.Fatal("cache insertion mutated episode list")
	}
	second.Episodes[0].Title = "mutated cache hit"
	third, err := service.Season(context.Background(), 12, 0, "zh-TW")
	if err != nil || third.Episodes[0].Title != "Special" {
		t.Fatal("cache hit mutated episode list")
	}
	for i := 0; i < 2; i++ {
		episode, err := service.Episode(context.Background(), 12, 0, 1, "zh-TW")
		if err != nil || episode.SeriesID != 12 || episode.SeasonNumber != 0 || episode.EpisodeNumber != 1 || episode.SourceURL != "https://www.themoviedb.org/tv/12/season/0/episode/1" {
			t.Fatal("episode tuple differs")
		}
	}
	if seasonCalls.Load() != 1 || episodeCalls.Load() != 1 {
		t.Fatalf("season=%d episode=%d", seasonCalls.Load(), episodeCalls.Load())
	}
	if _, err := service.Episode(context.Background(), 12, 0, 2, "zh-TW"); err != domain.ErrMetadataUnavailable {
		t.Fatal("wrong episode tuple accepted")
	}
}
