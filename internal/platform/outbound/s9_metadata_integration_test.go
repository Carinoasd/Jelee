package outbound_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/adapter/metadata"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

func TestSeasonEpisodeImagesThroughActualTLSAndApplication(t *testing.T) {
	key := strings.Repeat("d", 32)
	var seasons, episodes atomic.Int32
	provider := mappedTMDB(t, key, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Host != "api.themoviedb.org" || r.TLS == nil || q.Get("api_key") != key || q.Get("include_image_language") != "ja,null" || q.Get("language") != "" {
			t.Error("controlled season/episode image request differs")
		}
		switch r.URL.Path {
		case "/3/tv/1399/season/1/images":
			seasons.Add(1)
			_, _ = fmt.Fprint(w, `{"id":3624,"posters":[{"iso_639_1":"en","file_path":"/en.jpg","width":500,"height":750,"vote_average":9,"vote_count":9},{"iso_639_1":null,"file_path":"/none.jpg","width":500,"height":750,"vote_average":8,"vote_count":1},{"iso_639_1":"ja","file_path":"/ja.jpg","width":500,"height":750,"vote_average":1,"vote_count":1}]}`)
		case "/3/tv/1399/season/1/episode/2/images":
			episodes.Add(1)
			_, _ = fmt.Fprint(w, `{"id":63056,"stills":[{"iso_639_1":null,"file_path":"/still.jpg","width":1280,"height":720,"vote_average":5,"vote_count":3}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	service, err := app.NewMetadata(provider)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		season, err := service.EpisodeImages(context.Background(), 1399, 1, 0, []string{"ja", "null"})
		if err != nil || season.Resource != "season" || season.ProviderID != 3624 || season.SourceURL != "https://www.themoviedb.org/tv/1399/season/1" || len(season.Candidates) != 2 || season.Candidates[0].FilePath != "/ja.jpg" || season.Candidates[1].FilePath != "/none.jpg" || !season.Candidates[0].NeedsConfirmation {
			t.Fatalf("season=%+v error=%v", season, err)
		}
		episode, err := service.EpisodeImages(context.Background(), 1399, 1, 2, []string{"ja", "null"})
		if err != nil || episode.Resource != "episode" || episode.EpisodeNumber != 2 || len(episode.Candidates) != 1 || episode.Candidates[0].Kind != "still" || !episode.Candidates[0].NeedsConfirmation {
			t.Fatalf("episode=%+v error=%v", episode, err)
		}
	}
	if seasons.Load() != 1 || episodes.Load() != 1 {
		t.Fatalf("cache not shared: seasons=%d episodes=%d", seasons.Load(), episodes.Load())
	}
	if _, err := service.EpisodeImages(context.Background(), 1399, 9, 0, []string{"ja", "null"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing season error=%v", err)
	}
}

func TestExternalIDApplicationThroughActualTLS(t *testing.T) {
	key := strings.Repeat("e", 32)
	provider := mappedTMDB(t, key, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/find/Q190050" || r.URL.Query().Get("external_source") != "wikidata_id" || r.URL.Query().Get("language") != "zh-TW" {
			t.Error("guarded find request differs")
		}
		_, _ = fmt.Fprint(w, `{"movie_results":[{"id":550,"title":"鬥陣俱樂部","original_title":"Fight Club","overview":"簡介","release_date":"1999-10-15"}],"tv_results":[{"id":1399,"name":"劇集","original_name":"Series","overview":"","first_air_date":"2011-04-17"}]}`)
	})
	service, err := app.NewMetadata(provider)
	if err != nil {
		t.Fatal(err)
	}
	value, err := service.FindByExternalID(context.Background(), domain.ExternalSourceWikidata, "Q190050", "zh-TW")
	if err != nil || !value.NeedsConfirmation || len(value.Movies) != 1 || value.Movies[0].OverviewSource.RequestedLanguage != "zh-TW" || len(value.Series) != 1 || value.Series[0].OverviewSource.RequestedLanguage != "" || value.Series[0].SourceURL != "https://www.themoviedb.org/tv/1399" {
		t.Fatalf("value=%+v error=%v", value, err)
	}
}

func TestConfiguredTMDBBudgetThroughActualTLS(t *testing.T) {
	key := strings.Repeat("f", 32)
	var calls, active, peak atomic.Int32
	var mu sync.Mutex
	var starts []time.Time
	provider := mappedTMDB(t, key, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		n := active.Add(1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		active.Add(-1)
		_, _ = fmt.Fprintf(w, `{"id":%s,"title":"Movie","release_date":"2024-02-29"}`, strings.TrimPrefix(r.URL.Path, "/3/movie/"))
	})
	if err := provider.WithGovernance(metadata.Governance{RequestInterval: 100 * time.Millisecond, MaxConcurrent: 1, DailyLimit: 3}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for id := int32(1); id <= 3; id++ {
		wg.Go(func() {
			if _, err := provider.Movie(context.Background(), id, "en-US"); err != nil {
				t.Errorf("movie %d: %v", id, err)
			}
		})
	}
	wg.Wait()
	if peak.Load() != 1 || calls.Load() != 3 {
		t.Fatalf("peak=%d calls=%d", peak.Load(), calls.Load())
	}
	mu.Lock()
	for i := 1; i < len(starts); i++ {
		// Server-side arrival can trail the governed start slightly.
		if gap := starts[i].Sub(starts[i-1]); gap < 80*time.Millisecond {
			t.Fatalf("configured interval not enforced: gap=%v", gap)
		}
	}
	mu.Unlock()
	if _, err := provider.Movie(context.Background(), 4, "en-US"); err == nil || calls.Load() != 3 {
		t.Fatalf("daily limit reached the network: err=%v calls=%d", err, calls.Load())
	}
	if value, err := provider.Movie(context.Background(), 2, "en-US"); err != nil || value.ProviderID != 2 || calls.Load() != 3 {
		t.Fatal("cached answer unavailable after the daily limit")
	}
}
