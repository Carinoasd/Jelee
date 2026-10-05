package httpapi

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/MoYuanCN/Jelee/internal/access"
	"github.com/MoYuanCN/Jelee/internal/adapter/media"
)

const trackID = "33333333-3333-4333-8333-333333333333"

func TestExternalTrackRoutes(t *testing.T) {
	f := newFixture(t, true, true)
	root := t.TempDir()
	original := []byte("[Script Info]\r\nTitle: \xb2\xe2\xca\xd4\r\n<svg onload=alert(1)>\r\n")
	if err := os.WriteFile(filepath.Join(root, "Film.chs.ass"), original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Film.en.eac3"), original, 0o600); err != nil {
		t.Fatal(err)
	}
	f.resolver.track = func(ctx context.Context, p access.Principal, source string, kind media.TrackKind, track string) (media.Source, error) {
		if p.UserID != userID || p.SessionID != sessionID || p.Kind != access.ClientNative || source != sourceID || track != trackID {
			return media.Source{}, media.ErrNotFound
		}
		if kind == media.TrackSubtitle {
			return media.Source{Root: root, RelativePath: "Film.chs.ass", Charset: "GB18030", ContentType: "text/html"}, nil
		}
		return media.Source{Root: root, RelativePath: "Film.en.eac3"}, nil
	}
	subtitles := "/api/v1/sources/" + sourceID + "/subtitles/" + trackID
	audio := "/api/v1/sources/" + sourceID + "/audio/" + trackID
	native := strings.Repeat("n", 43)

	for _, tc := range []struct{ path, contentType string }{{subtitles, "text/x-ssa; charset=GB18030"}, {audio, "audio/eac3"}} {
		r := httptest.NewRequest(http.MethodGet, "http://localhost"+tc.path, nil)
		r.Header.Set("Authorization", "Bearer "+native)
		r.Header.Set("Range", "bytes=0-12")
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		h := w.Header()
		if w.Code != 206 || w.Body.String() != string(original[:13]) || h.Get("Content-Type") != tc.contentType ||
			h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Security-Policy") != "sandbox; default-src 'none'" || h.Get("Content-Disposition") != "" {
			t.Fatalf("%s: %d %q %v", tc.path, w.Code, w.Body.String(), h)
		}
		head := f.request(http.MethodHead, tc.path, native)
		if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Length") != strconv.Itoa(len(original)) || head.Header().Get("Content-Type") != tc.contentType {
			t.Fatalf("%s HEAD: %d %v", tc.path, head.Code, head.Header())
		}
	}
	for _, name := range []string{"Film.chs.ass", "Film.en.eac3"} {
		after, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || sha256.Sum256(after) != sha256.Sum256(original) {
			t.Fatal("track delivery changed the original file")
		}
	}

	calls := f.resolver.calls
	// Web sessions, by bearer or by cookie, and spoofed client headers never
	// reach the lookup.
	for _, path := range []string{subtitles, audio} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			r := httptest.NewRequest(method, "http://localhost"+path, nil)
			r.Header.Set("Authorization", "Bearer "+strings.Repeat("w", 43))
			r.Header.Set("X-Client-Kind", "native")
			w := httptest.NewRecorder()
			f.handler.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatalf("%s %s web bearer: %d", method, path, w.Code)
			}
			r = httptest.NewRequest(method, "http://localhost"+path, nil)
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: webToken})
			w = httptest.NewRecorder()
			f.handler.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatalf("%s %s web cookie: %d", method, path, w.Code)
			}
		}
		r := httptest.NewRequest(http.MethodGet, "http://localhost"+path, nil)
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: webToken})
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, r)
		assertProblem(t, w, 403, "web_playback_disabled")
		assertProblem(t, f.request(http.MethodGet, path, ""), 401, "authentication_required")
		assertProblem(t, f.request(http.MethodGet, path+"?subtitleCodec=webvtt", native), 409, "transcode_disabled")
		assertProblem(t, f.request(http.MethodGet, path+"?subtitleMethod=Encode", native), 409, "transcode_disabled")
		assertProblem(t, f.request(http.MethodGet, path+"?charset=UTF-8", native), 400, "invalid_request")
		assertProblem(t, f.request(http.MethodPost, path, native), 405, "method_not_allowed")
	}
	for _, path := range []string{
		"/api/v1/sources/" + sourceID + "/subtitles/not-a-uuid",
		"/api/v1/sources/not-a-uuid/audio/" + trackID,
		"/api/v1/sources/" + sourceID + "/subtitles/" + trackID + "/extra",
		"/api/v1/sources/" + sourceID + "/video/" + trackID,
	} {
		assertProblem(t, f.request(http.MethodGet, path, native), 404, "not_found")
	}
	if f.resolver.calls != calls {
		t.Fatal("refused track request reached the resolver")
	}
	// A track of another source and an unknown track are answered alike.
	for _, path := range []string{
		"/api/v1/sources/" + userID + "/subtitles/" + trackID,
		"/api/v1/sources/" + sourceID + "/audio/" + userID,
	} {
		assertProblem(t, f.request(http.MethodGet, path, native), 404, "not_found")
	}
}

func TestExternalTrackRoutesFollowRolloutFlags(t *testing.T) {
	f := newFixture(t, true, false)
	assertProblem(t, f.request(http.MethodGet, "/api/v1/sources/"+sourceID+"/subtitles/"+trackID, strings.Repeat("n", 43)), 404, "not_found")
	if f.resolver.calls != 0 {
		t.Fatal("disabled track route reached the resolver")
	}
}
