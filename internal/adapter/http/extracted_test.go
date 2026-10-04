package httpapi

import (
	"context"
	"crypto/sha256"
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
	"github.com/MoYuanCN/Jelee/internal/platform/config"
)

type fakeExtracted struct {
	root      string
	available bool
	calls     int
	kinds     []media.ExtractedKind
}

func (f *fakeExtracted) Available() bool { return f.available }

func (f *fakeExtracted) ResolveExtracted(_ context.Context, p access.Principal, source string, kind media.ExtractedKind, index int) (media.Source, error) {
	f.calls++
	f.kinds = append(f.kinds, kind)
	if p.Kind != access.ClientNative || source != sourceID {
		return media.Source{}, media.ErrNotFound
	}
	switch {
	case kind == media.ExtractedSubtitle && index == 2:
		return media.Source{Root: f.root, RelativePath: sourceID + "/rev/t2.ass", ContentType: "text/html", ETag: `"ignored"`}, nil
	case kind == media.ExtractedAttachment && index == 1:
		return media.Source{Root: f.root, RelativePath: sourceID + "/rev/a1.ttf"}, nil
	}
	return media.Source{}, media.ErrNotFound
}

func extractedFixture(t *testing.T, enabled bool, extracted *fakeExtracted) *fixture {
	t.Helper()
	f := &fixture{backend: &fakeBackend{}, repository: &fakeRepository{}, resolver: &fakeResolver{}}
	cfg := validConfig()
	cfg.EnableCatalog, cfg.EnableDirect = true, true
	if enabled {
		cfg.Matroska = configMatroska(t)
	}
	var options []Option
	if extracted != nil {
		options = append(options, WithExtracted(extracted))
	}
	handler, err := NewWithImages(cfg, f.backend, app.NewCatalog(f.repository), f.resolver, slog.Default(), nil, nil, nil, nil, nil, options...)
	if err != nil {
		t.Fatal(err)
	}
	f.handler = handler
	return f
}

func TestExtractedRoutesDeliverCachedItemsUnconvertedToNativeSessionsOnly(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, sourceID, "rev")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	ass := []byte("\xef\xbb\xbf[Script Info]\r\nTitle: <svg onload=alert(1)>\r\n")
	font := []byte("synthetic font bytes")
	if os.WriteFile(filepath.Join(directory, "t2.ass"), ass, 0o600) != nil || os.WriteFile(filepath.Join(directory, "a1.ttf"), font, 0o600) != nil {
		t.Fatal("write cache fixture")
	}
	extracted := &fakeExtracted{root: root, available: true}
	f := extractedFixture(t, true, extracted)
	native := strings.Repeat("n", 43)
	for _, tc := range []struct {
		path, contentType string
		body              []byte
	}{
		{"/api/v1/sources/" + sourceID + "/embedded-subtitles/2", "text/x-ssa; charset=UTF-8", ass},
		{"/api/v1/sources/" + sourceID + "/attachments/1", "font/ttf", font},
	} {
		w := f.request(http.MethodGet, tc.path, native)
		h := w.Header()
		if w.Code != 200 || w.Body.String() != string(tc.body) || h.Get("Content-Type") != tc.contentType || h.Get("ETag") != "" ||
			h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Security-Policy") != "sandbox; default-src 'none'" {
			t.Fatalf("%s: %d %q %v", tc.path, w.Code, w.Body.String(), h)
		}
		if head := f.request(http.MethodHead, tc.path, native); head.Code != 200 || head.Body.Len() != 0 {
			t.Fatalf("%s HEAD: %d", tc.path, head.Code)
		}
		// Web sessions and anonymous callers never reach the resolver.
		calls := extracted.calls
		assertProblem(t, f.request(http.MethodGet, tc.path, webToken), 403, "web_playback_disabled")
		assertProblem(t, f.request(http.MethodGet, tc.path, ""), 401, "authentication_required")
		assertProblem(t, f.request(http.MethodGet, tc.path+"?subtitleCodec=webvtt", native), 409, "transcode_disabled")
		assertProblem(t, f.request(http.MethodGet, tc.path+"?charset=UTF-8", native), 400, "invalid_request")
		assertProblem(t, f.request(http.MethodPost, tc.path, native), 405, "method_not_allowed")
		if extracted.calls != calls {
			t.Fatal("a refused request reached the extraction resolver")
		}
	}
	after, err := os.ReadFile(filepath.Join(directory, "t2.ass"))
	if err != nil || sha256.Sum256(after) != sha256.Sum256(ass) {
		t.Fatal("delivery changed the cached copy")
	}
	calls := extracted.calls
	for _, path := range []string{
		"/api/v1/sources/" + sourceID + "/embedded-subtitles/01",
		"/api/v1/sources/" + sourceID + "/embedded-subtitles/-1",
		"/api/v1/sources/" + sourceID + "/embedded-subtitles/4097",
		"/api/v1/sources/" + sourceID + "/embedded-subtitles/x",
		"/api/v1/sources/" + sourceID + "/attachments/0",
		"/api/v1/sources/not-a-uuid/attachments/1",
	} {
		assertProblem(t, f.request(http.MethodGet, path, native), 404, "not_found")
	}
	if extracted.calls != calls {
		t.Fatal("a malformed path reached the extraction resolver")
	}
	// Hidden, missing and non-extractable items look alike.
	assertProblem(t, f.request(http.MethodGet, "/api/v1/sources/"+sourceID+"/embedded-subtitles/3", native), 404, "not_found")
	assertProblem(t, f.request(http.MethodGet, "/api/v1/sources/55555555-5555-4555-8555-555555555555/attachments/1", native), 404, "not_found")
}

func TestExtractedRoutesFollowConfiguration(t *testing.T) {
	native := strings.Repeat("n", 43)
	// Disabled: no route, and a wired resolver is ignored.
	extracted := &fakeExtracted{available: true}
	f := extractedFixture(t, false, extracted)
	assertProblem(t, f.request(http.MethodGet, "/api/v1/sources/"+sourceID+"/embedded-subtitles/2", native), 404, "not_found")
	if extracted.calls != 0 {
		t.Fatal("disabled extraction reached the resolver")
	}
	// Enabled without a runtime: routes exist and answer like missing items.
	f = extractedFixture(t, true, nil)
	assertProblem(t, f.request(http.MethodGet, "/api/v1/sources/"+sourceID+"/attachments/1", native), 404, "not_found")
}

func TestPlaybackDecoratesOnlyAvailableExtraction(t *testing.T) {
	source := domain.PlaybackSource{ID: sourceID, Subtitles: []domain.PlaybackSubtitleTrack{{Index: 2, Extractable: true}, {Index: 3}},
		Attachments: []domain.PlaybackAttachment{{ID: 1, Font: true}, {ID: 2}}}
	s := &Server{extracted: &fakeExtracted{available: false}}
	unavailable := source
	unavailable.Subtitles = append([]domain.PlaybackSubtitleTrack(nil), source.Subtitles...)
	s.decorateExtracted(&unavailable)
	if unavailable.Subtitles[0].URL != "" {
		t.Fatal("unavailable extraction advertised a URL")
	}
	s.extracted = &fakeExtracted{available: true}
	s.decorateExtracted(&source)
	if source.Subtitles[0].URL != "/api/v1/sources/"+sourceID+"/embedded-subtitles/2" || source.Subtitles[1].URL != "" ||
		source.Attachments[0].URL != "/api/v1/sources/"+sourceID+"/attachments/1" || source.Attachments[1].URL != "" {
		t.Fatalf("decorated %+v", source)
	}
	if !(hiddenExtracted{&fakeExtracted{available: true}}).Available() || (hiddenExtracted{unavailableExtracted{}}).Available() {
		t.Fatal("hidden status wrapper changed availability")
	}
	if _, err := (hiddenExtracted{&fakeExtracted{}}).ResolveExtracted(context.Background(), access.Principal{Kind: access.ClientNative}, "missing", media.ExtractedSubtitle, 1); err != domain.ErrForbidden {
		t.Fatal("hidden status opt-in not applied")
	}
}

func configMatroska(t *testing.T) config.MatroskaConfig {
	t.Helper()
	return config.MatroskaConfig{EnableExtraction: true, CacheRoot: t.TempDir(), CacheMaxBytes: 1 << 30}
}
