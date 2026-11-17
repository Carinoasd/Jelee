package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
	"github.com/MoYuanCN/Jelee/internal/app"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type fakeUTF8 struct {
	root      string
	available bool
	calls     int
}

func (f *fakeUTF8) Available() bool { return f.available }

func (f *fakeUTF8) ResolveUTF8Track(_ context.Context, p access.Principal, source, track string) (media.Source, error) {
	f.calls++
	if p.Kind != access.ClientNative || source != sourceID {
		return media.Source{}, media.ErrNotFound
	}
	if track != trackID {
		return media.Source{}, media.ErrNotDerived
	}
	return media.Source{Root: f.root, RelativePath: "copy.srt", ContentType: "text/html", ETag: `"ignored"`}, nil
}

func utf8Fixture(t *testing.T, enabled bool, utf8 *fakeUTF8) *fixture {
	t.Helper()
	f := &fixture{backend: &fakeBackend{}, repository: &fakeRepository{}, resolver: &fakeResolver{}}
	cfg := validConfig()
	cfg.EnableCatalog, cfg.EnableDirect = true, true
	if enabled {
		cfg.Matroska = configMatroska(t)
	}
	var options []Option
	if utf8 != nil {
		options = append(options, WithUTF8Subtitles(utf8))
	}
	handler, err := NewWithImages(cfg, f.backend, app.NewCatalog(f.repository), f.resolver, slog.Default(), nil, nil, nil, nil, nil, options...)
	if err != nil {
		t.Fatal(err)
	}
	f.handler = handler
	return f
}

func TestUTF8SubtitleRouteDeliversCopiesToNativeSessionsOnly(t *testing.T) {
	root := t.TempDir()
	copied := []byte("1\r\n00:00:01,000 --> 00:00:02,000\r\n你好\r\n")
	if err := os.WriteFile(filepath.Join(root, "copy.srt"), copied, 0o600); err != nil {
		t.Fatal(err)
	}
	utf8 := &fakeUTF8{root: root, available: true}
	f := utf8Fixture(t, true, utf8)
	native := strings.Repeat("n", 43)
	path := "/api/v1/sources/" + sourceID + "/subtitles/" + trackID + "/utf8"
	w := f.request(http.MethodGet, path, native)
	h := w.Header()
	if w.Code != 200 || w.Body.String() != string(copied) || h.Get("Content-Type") != "application/x-subrip; charset=UTF-8" || h.Get("ETag") != "" ||
		h.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("%d %q %v", w.Code, w.Body.String(), h)
	}
	if head := f.request(http.MethodHead, path, native); head.Code != 200 || head.Body.Len() != 0 {
		t.Fatalf("HEAD: %d", head.Code)
	}
	calls := utf8.calls
	assertProblem(t, f.request(http.MethodGet, path, webToken), 403, "web_playback_disabled")
	assertProblem(t, f.request(http.MethodGet, path, ""), 401, "authentication_required")
	assertProblem(t, f.request(http.MethodGet, path+"?charset=GBK", native), 400, "invalid_request")
	assertProblem(t, f.request(http.MethodGet, path+"?subtitleCodec=srt", native), 409, "transcode_disabled")
	assertProblem(t, f.request(http.MethodGet, "/api/v1/sources/"+sourceID+"/subtitles/not-a-uuid/utf8", native), 404, "not_found")
	if utf8.calls != calls {
		t.Fatal("a refused request reached the resolver")
	}
	// No copy (unknown charset, withheld conversion) and a hidden track
	// look like a missing track; the original route is untouched.
	assertProblem(t, f.request(http.MethodGet, "/api/v1/sources/"+sourceID+"/subtitles/44444444-4444-4444-8444-444444444444/utf8", native), 404, "not_found")
	assertProblem(t, f.request(http.MethodGet, "/api/v1/sources/55555555-5555-4555-8555-555555555555/subtitles/"+trackID+"/utf8", native), 404, "not_found")
	// Disabled subtitle cache: no route, a wired resolver is ignored.
	disabled := &fakeUTF8{root: root, available: true}
	assertProblem(t, utf8Fixture(t, false, disabled).request(http.MethodGet, path, native), 404, "not_found")
	if disabled.calls != 0 {
		t.Fatal("disabled cache reached the resolver")
	}
	// Enabled without a resolver: the route answers like a missing track.
	assertProblem(t, utf8Fixture(t, true, nil).request(http.MethodGet, path, native), 404, "not_found")
}

func TestPlaybackOffersUTF8CopiesOnlyForConvertibleCharsets(t *testing.T) {
	source := domain.PlaybackSource{ID: sourceID, External: []domain.PlaybackExternalTrack{
		{ID: trackID, Kind: domain.SidecarKindSubtitle, CharsetState: domain.SidecarCharsetConvertible},
		{ID: "a", Kind: domain.SidecarKindSubtitle, CharsetState: domain.SidecarCharsetUndetermined},
		{ID: "b", Kind: domain.SidecarKindSubtitle, CharsetState: domain.SidecarCharsetUTF8},
		{ID: "c", Kind: domain.SidecarKindAudio},
	}}
	s := &Server{utf8: &fakeUTF8{available: false}}
	unavailable := source
	unavailable.External = append([]domain.PlaybackExternalTrack(nil), source.External...)
	s.decorateUTF8(&unavailable)
	if unavailable.External[0].UTF8URL != "" {
		t.Fatal("unavailable cache advertised a copy")
	}
	s.utf8 = &fakeUTF8{available: true}
	s.decorateUTF8(&source)
	if source.External[0].UTF8URL != "/api/v1/sources/"+sourceID+"/subtitles/"+trackID+"/utf8" {
		t.Fatalf("convertible: %+v", source.External[0])
	}
	for _, track := range source.External[1:] {
		if track.UTF8URL != "" {
			t.Fatalf("offered a copy for %+v", track)
		}
	}
	if !(hiddenUTF8{&fakeUTF8{available: true}}).Available() || (hiddenUTF8{unavailableUTF8{}}).Available() {
		t.Fatal("hidden status wrapper changed availability")
	}
	if _, err := (hiddenUTF8{&fakeUTF8{}}).ResolveUTF8Track(context.Background(), access.Principal{Kind: access.ClientNative}, "missing", trackID); err != domain.ErrForbidden {
		t.Fatal("hidden status opt-in not applied")
	}
}
