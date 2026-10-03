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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/metadata"
	"github.com/MoYuanCN/Jelee/internal/domain"
	"github.com/MoYuanCN/Jelee/internal/platform/outbound"
)

func mappedTMDB(t *testing.T, key string, handler http.HandlerFunc) *metadata.TMDB {
	t.Helper()
	cert, roots := providerCertificate(t)
	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)
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
	t.Cleanup(provider.Close)
	return provider
}

func TestConcurrentMovieMissesThroughActualTLSShareOneRequest(t *testing.T) {
	key := strings.Repeat("b", 32)
	var calls atomic.Int32
	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	provider := mappedTMDB(t, key, func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "api.themoviedb.org" || r.URL.Path != "/3/movie/12" || r.URL.Query().Get("api_key") != key {
			t.Error("guarded movie request differs")
		}
		calls.Add(1)
		select {
		case arrived <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, `{"id":12,"title":"電影","release_date":"2024-02-29"}`)
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			if value, err := provider.Movie(context.Background(), 12, "zh-TW"); err != nil || value.Title != "電影" {
				t.Errorf("candidate=%+v error=%v", value, err)
			}
		})
	}
	<-arrived
	// Late callers either join the flight or hit the cache; both keep one request.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("upstream movie requests=%d", calls.Load())
	}
}

func TestFindByExternalIDThroughActualTLSRetryAndCache(t *testing.T) {
	key := strings.Repeat("c", 32)
	var calls atomic.Int32
	provider := mappedTMDB(t, key, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Host != "api.themoviedb.org" || r.TLS == nil || r.URL.Path != "/3/find/tt0137523" || q.Get("api_key") != key || q.Get("external_source") != "imdb_id" || q.Get("language") != "en-US" {
			t.Error("guarded find request differs")
		}
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{"movie_results":[{"id":550,"title":"Fight Club","release_date":"1999-10-15"}],"tv_results":[],"person_results":[]}`)
	})
	started := time.Now()
	value, err := provider.FindByExternalID(context.Background(), domain.ExternalSourceIMDb, "tt0137523", "en-US")
	if err != nil || len(value.Movies) != 1 || value.Movies[0].ProviderID != 550 || value.Movies[0].SourceURL != "https://www.themoviedb.org/movie/550" || len(value.Series) != 0 {
		t.Fatalf("value=%+v error=%v", value, err)
	}
	if time.Since(started) < time.Second {
		t.Fatal("Retry-After was not honoured")
	}
	if _, err := provider.FindByExternalID(context.Background(), domain.ExternalSourceIMDb, "tt0137523", "en-US"); err != nil || calls.Load() != 2 {
		t.Fatalf("find cache error=%v calls=%d", err, calls.Load())
	}
}
