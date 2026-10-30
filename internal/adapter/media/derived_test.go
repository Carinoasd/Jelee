package media

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/access"
)

type utf8Func func(context.Context, access.Principal, string, string) (Source, error)

func (f utf8Func) ResolveUTF8Track(ctx context.Context, p access.Principal, sourceID, trackID string) (Source, error) {
	return f(ctx, p, sourceID, trackID)
}

// The UTF-8 copy is served with a UTF-8 charset parameter; without one the
// native route answers like a missing track and the compatibility route
// falls back to the original; web sessions get neither.
func TestServeUTF8Track(t *testing.T) {
	root := t.TempDir()
	original := []byte{'1', '\n', 0xc4, 0xe3, '\n'}
	if err := os.WriteFile(filepath.Join(root, "original.srt"), original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "copy.srt"), []byte("1\n你\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := testHandler(t, resolveFunc(func(_ context.Context, _ access.Principal, id string) (Source, error) {
		if id != "track" {
			return Source{}, ErrNotFound
		}
		return Source{Root: root, RelativePath: "original.srt", Charset: "GB18030", ContentType: "text/html"}, nil
	}), 2)
	copied := utf8Func(func(_ context.Context, _ access.Principal, _, trackID string) (Source, error) {
		if trackID != "track" {
			return Source{}, ErrNotFound
		}
		return Source{Root: root, RelativePath: "copy.srt", ETag: `"x"`, ContentType: "text/html"}, nil
	})
	none := utf8Func(func(context.Context, access.Principal, string, string) (Source, error) {
		return Source{}, ErrNotDerived
	})
	for _, tc := range []struct {
		name     string
		resolver UTF8Resolver
		track    string
		fallback bool
		code     int
		body     string
		ctype    string
	}{
		{"copy", copied, "track", false, 200, "1\n你\n", "application/x-subrip; charset=UTF-8"},
		{"copy compat", copied, "track", true, 200, "1\n你\n", "application/x-subrip; charset=UTF-8"},
		{"none native", none, "track", false, 404, "", ""},
		{"none compat", none, "track", true, 200, string(original), "application/x-subrip; charset=GB18030"},
		{"missing", copied, "other", true, 404, "", ""},
		{"nil resolver", nil, "track", true, 404, "", ""},
	} {
		w := httptest.NewRecorder()
		handler.ServeUTF8Track(w, nativeRequest(http.MethodGet, "/x"), tc.resolver, "source", tc.track, tc.fallback)
		if w.Code != tc.code || tc.code == 200 && (w.Body.String() != tc.body || w.Header().Get("Content-Type") != tc.ctype) {
			t.Errorf("%s: %d %q %q", tc.name, w.Code, w.Body.String(), w.Header().Get("Content-Type"))
		}
		if tc.name == "copy" && w.Header().Get("ETag") != "" {
			t.Errorf("copy carries an ETag: %q", w.Header().Get("ETag"))
		}
	}
	web := httptest.NewRequest(http.MethodGet, "/x", nil)
	web = web.WithContext(access.WithPrincipal(web.Context(), access.Principal{UserID: "u", SessionID: "s", Kind: access.ClientWeb, Admin: true}))
	w := httptest.NewRecorder()
	handler.ServeUTF8Track(w, web, copied, "source", "track", true)
	if w.Code != http.StatusForbidden {
		t.Fatalf("web session: %d", w.Code)
	}
}

type sidecarOCRFunc func(context.Context, access.Principal, string, string) (Source, error)

func (f sidecarOCRFunc) ResolveSidecarOCR(ctx context.Context, p access.Principal, sourceID, trackID string) (Source, error) {
	return f(ctx, p, sourceID, trackID)
}

// The SRT OCR derived from an external bitmap subtitle is served with the
// fixed SubRip type and no ETag, to native sessions only.
func TestServeSidecarOCR(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "s0.srt"), []byte("1\n00:00:01,000 --> 00:00:02,000\nocr\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := testHandler(t, resolveFunc(func(context.Context, access.Principal, string) (Source, error) { return Source{}, ErrNotFound }), 2)
	calls := 0
	resolver := sidecarOCRFunc(func(_ context.Context, _ access.Principal, sourceID, trackID string) (Source, error) {
		calls++
		if sourceID != "source" || trackID != "track" {
			return Source{}, ErrNotFound
		}
		return Source{Root: root, RelativePath: "s0.srt", ETag: `"x"`, ContentType: "image/x-pgs"}, nil
	})
	for _, tc := range []struct {
		name     string
		resolver SidecarOCRResolver
		track    string
		code     int
	}{
		{"ready", resolver, "track", 200},
		{"missing", resolver, "other", 404},
		{"nil resolver", nil, "track", 404},
		{"no track", resolver, "", 404},
	} {
		w := httptest.NewRecorder()
		handler.ServeSidecarOCR(w, nativeRequest(http.MethodGet, "/x"), tc.resolver, "source", tc.track)
		if w.Code != tc.code || tc.code == 200 && (w.Header().Get("Content-Type") != "application/x-subrip; charset=UTF-8" || w.Header().Get("ETag") != "" || w.Body.String() != "1\n00:00:01,000 --> 00:00:02,000\nocr\n") {
			t.Errorf("%s: %d %q %v", tc.name, w.Code, w.Body.String(), w.Header())
		}
	}
	before := calls
	web := httptest.NewRequest(http.MethodGet, "/x", nil)
	web = web.WithContext(access.WithPrincipal(web.Context(), access.Principal{UserID: "u", SessionID: "s", Kind: access.ClientWeb, Admin: true}))
	w := httptest.NewRecorder()
	handler.ServeSidecarOCR(w, web, resolver, "source", "track")
	if w.Code != http.StatusForbidden || calls != before {
		t.Fatalf("web session: %d, resolver calls %d", w.Code, calls-before)
	}
}
