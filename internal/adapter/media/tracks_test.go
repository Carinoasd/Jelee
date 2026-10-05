package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoYuanCN/Jelee/internal/access"
)

// trackResolver serves one source and one track of it. It records which
// lookup ran so tests can prove a refused request never reached storage.
type trackResolver struct {
	source, track         Source
	sourceID, trackID     string
	kind                  TrackKind
	sourceCalls, tracks   atomic.Int32
	lastSource, lastTrack string
}

func (r *trackResolver) Resolve(_ context.Context, _ access.Principal, id string) (Source, error) {
	r.sourceCalls.Add(1)
	if id != r.sourceID {
		return Source{}, ErrNotFound
	}
	return r.source, nil
}

func (r *trackResolver) ResolveTrack(_ context.Context, p access.Principal, sourceID string, kind TrackKind, trackID string) (Source, error) {
	r.tracks.Add(1)
	r.lastSource, r.lastTrack = sourceID, trackID
	if p.UserID != "u" || sourceID != r.sourceID || kind != r.kind || trackID != r.trackID {
		return Source{}, ErrNotFound
	}
	return r.track, nil
}

// trackFixture writes a subtitle next to the video fixture. The bytes are
// Shift_JIS with a BOM-less CRLF layout so any conversion would show.
func trackFixture(t *testing.T, name string) (*trackResolver, []byte) {
	t.Helper()
	source, _ := fixture(t)
	content := append([]byte("1\r\n00:00:01,000 --> 00:00:02,000\r\n"), 0x82, 0xb1, 0x82, 0xf1, '\r', '\n')
	content = append(content, bytes.Repeat([]byte("<script>alert(1)</script>\r\n"), 4000)...)
	if err := os.WriteFile(filepath.Join(source.Root, name), content, 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(source.Root, name), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	track := Source{Root: source.Root, RelativePath: name, ContentType: "text/html", ETag: `"track-rev-1"`, Charset: "Shift_JIS"}
	return &trackResolver{source: source, track: track, sourceID: "source", trackID: "track", kind: TrackSubtitle}, content
}

func TestTrackDeliveryRangeHeadETagAndUnchanged(t *testing.T) {
	resolver, content := trackFixture(t, "original.ja.srt")
	before := sha256.Sum256(content)
	handler := testHandler(t, resolver, 2)
	cases := []struct {
		name, method, ranges, ifRange, ifNoneMatch string
		status                                     int
		want                                       []byte
	}{
		{"complete", "GET", "", "", "", 200, content},
		{"head", "HEAD", "", "", "", 200, nil},
		{"first", "GET", "bytes=0-9", "", "", 206, content[:10]},
		{"suffix", "GET", "bytes=-7", "", "", 206, content[len(content)-7:]},
		{"open_end", "GET", "bytes=40-", "", "", 206, content[40:]},
		{"head_range", "HEAD", "bytes=0-9", "", "", 206, nil},
		{"etag_match", "GET", "bytes=0-9", `"track-rev-1"`, "", 206, content[:10]},
		{"etag_mismatch", "GET", "bytes=0-9", `"older"`, "", 200, content},
		{"not_modified", "GET", "", "", `"track-rev-1"`, 304, nil},
		{"date_match", "GET", "bytes=0-9", "Thu, 02 Jan 2025 03:04:05 GMT", "", 206, content[:10]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := nativeRequest(tc.method, "/track")
			r.Header.Set("Range", tc.ranges)
			r.Header.Set("If-Range", tc.ifRange)
			r.Header.Set("If-None-Match", tc.ifNoneMatch)
			w := httptest.NewRecorder()
			w.Header().Set("Content-Disposition", "attachment; filename=original.ja.srt")
			handler.ServeTrack(w, r, "source", TrackSubtitle, "track")
			if w.Code != tc.status || !bytes.Equal(w.Body.Bytes(), tc.want) {
				t.Fatalf("status=%d bytes=%d; want status=%d bytes=%d", w.Code, w.Body.Len(), tc.status, len(tc.want))
			}
			h := w.Header()
			if h.Get("Content-Disposition") != "" || h.Get("ETag") != `"track-rev-1"` || h.Get("X-Content-Type-Options") != "nosniff" ||
				h.Get("Content-Security-Policy") != mediaContentSecurityPolicy || h.Get("Cache-Control") != "private, no-store" {
				t.Fatalf("unexpected headers: %v", h)
			}
			if tc.status != 304 && h.Get("Content-Type") != "application/x-subrip; charset=Shift_JIS" {
				t.Fatalf("content type %q: the resolver's type must be ignored", h.Get("Content-Type"))
			}
			if tc.method == "HEAD" && tc.status == 200 && h.Get("Content-Length") != strconv.Itoa(len(content)) {
				t.Fatalf("HEAD length %q", h.Get("Content-Length"))
			}
		})
	}
	if resolver.sourceCalls.Load() != 0 || resolver.lastSource != "source" || resolver.lastTrack != "track" {
		t.Fatal("track lookup did not receive the source and track IDs")
	}
	actual, err := os.ReadFile(filepath.Join(resolver.track.Root, resolver.track.RelativePath))
	if err != nil || sha256.Sum256(actual) != before {
		t.Fatalf("original track changed: %v", err)
	}
}

func TestTrackDeliveryRefusalsAndMisses(t *testing.T) {
	resolver, _ := trackFixture(t, "original.ja.srt")
	handler := testHandler(t, resolver, 2)
	web := httptest.NewRequest("GET", "/track", nil)
	web = web.WithContext(access.WithPrincipal(web.Context(), access.Principal{UserID: "u", SessionID: "s", Kind: access.ClientWeb, Admin: true}))
	unknown := httptest.NewRequest("GET", "/track", nil)
	unknown = unknown.WithContext(access.WithPrincipal(unknown.Context(), access.Principal{UserID: "u", SessionID: "s"}))
	transcode := nativeRequest("GET", "/track?subtitleCodec=webvtt")
	for _, tc := range []struct {
		name          string
		r             *http.Request
		source, track string
		kind          TrackKind
		status        int
		lookups       int32
	}{
		{"anonymous", httptest.NewRequest("GET", "/track", nil), "source", "track", TrackSubtitle, 401, 0},
		{"web_admin", web, "source", "track", TrackSubtitle, 403, 0},
		{"unknown_kind_session", unknown, "source", "track", TrackSubtitle, 403, 0},
		{"conversion_parameter", transcode, "source", "track", TrackSubtitle, 409, 0},
		{"post", nativeRequest("POST", "/track"), "source", "track", TrackSubtitle, 405, 0},
		{"invalid_kind", nativeRequest("GET", "/track"), "source", "track", TrackKind("video"), 404, 0},
		{"empty_track", nativeRequest("GET", "/track"), "source", "", TrackSubtitle, 404, 0},
		{"wrong_kind", nativeRequest("GET", "/track"), "source", "track", TrackAudio, 404, 1},
		{"foreign_source", nativeRequest("GET", "/track"), "other", "track", TrackSubtitle, 404, 1},
		{"missing_track", nativeRequest("GET", "/track"), "source", "missing", TrackSubtitle, 404, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := resolver.tracks.Load()
			w := httptest.NewRecorder()
			handler.ServeTrack(w, tc.r, tc.source, tc.kind, tc.track)
			if w.Code != tc.status || resolver.tracks.Load()-before != tc.lookups {
				t.Fatalf("status=%d lookups=%d; want %d/%d", w.Code, resolver.tracks.Load()-before, tc.status, tc.lookups)
			}
			if w.Header().Get("Content-Security-Policy") == mediaContentSecurityPolicy || strings.Contains(w.Body.String(), "script") {
				t.Fatalf("refusal carried media headers or bytes: %v", w.Header())
			}
		})
	}
	// A file that disappeared after the scan is a miss, not a path leak.
	resolver.track.RelativePath = "gone.srt"
	w := httptest.NewRecorder()
	handler.ServeTrack(w, nativeRequest("GET", "/track"), "source", TrackSubtitle, "track")
	if w.Code != 404 || strings.Contains(w.Body.String(), "gone") {
		t.Fatalf("missing file: %d %q", w.Code, w.Body.String())
	}
}

func TestTrackContentTypes(t *testing.T) {
	for _, tc := range []struct {
		kind               TrackKind
		ext, charset, want string
	}{
		{TrackSubtitle, ".srt", "", "application/x-subrip"},
		{TrackSubtitle, ".SRT", "UTF-8", "application/x-subrip; charset=UTF-8"},
		{TrackSubtitle, "ass", "GB18030", "text/x-ssa; charset=GB18030"},
		{TrackSubtitle, ".ssa", "", "text/x-ssa"},
		{TrackSubtitle, ".vtt", "UTF-8", "text/vtt; charset=UTF-8"},
		{TrackSubtitle, ".webvtt", "", "text/vtt"},
		{TrackSubtitle, ".ttml", "", "application/ttml+xml"},
		{TrackSubtitle, ".dfxp", "UTF-16LE", "application/ttml+xml; charset=UTF-16LE"},
		{TrackSubtitle, ".smi", "EUC-KR", "application/x-sami; charset=EUC-KR"},
		{TrackSubtitle, ".idx", "", "text/plain"},
		{TrackSubtitle, ".sub", "", "application/octet-stream"},
		{TrackSubtitle, ".sub", "Big5", "text/x-microdvd; charset=Big5"},
		{TrackSubtitle, ".sup", "", "application/x-pgs"},
		{TrackSubtitle, ".sup", "UTF-8", "application/x-pgs"},
		{TrackSubtitle, ".srt", "x-evil\r\nSet-Cookie: a=b", "application/x-subrip"},
		{TrackSubtitle, ".srt", "utf-8", "application/x-subrip"},
		{TrackSubtitle, ".html", "UTF-8", "application/octet-stream"},
		{TrackSubtitle, ".svg", "", "application/octet-stream"},
		{TrackSubtitle, ".flac", "", "application/octet-stream"},
		{TrackAudio, ".mka", "", "audio/x-matroska"},
		{TrackAudio, ".ac3", "", "audio/ac3"},
		{TrackAudio, ".eac3", "", "audio/eac3"},
		{TrackAudio, ".ec3", "", "audio/eac3"},
		{TrackAudio, ".dts", "", "audio/vnd.dts"},
		{TrackAudio, ".dtshd", "", "audio/vnd.dts.hd"},
		{TrackAudio, ".truehd", "", "audio/vnd.dolby.mlp"},
		{TrackAudio, ".flac", "", "audio/flac"},
		{TrackAudio, ".aac", "", "audio/aac"},
		{TrackAudio, ".m4a", "", "audio/mp4"},
		{TrackAudio, ".opus", "", "audio/opus"},
		{TrackAudio, ".ogg", "", "audio/ogg"},
		{TrackAudio, ".mp3", "", "audio/mpeg"},
		{TrackAudio, ".wav", "", "audio/wav"},
		{TrackAudio, ".flac", "UTF-8", "audio/flac"},
		{TrackAudio, ".alac", "", "application/octet-stream"},
		{TrackAudio, ".srt", "UTF-8", "application/octet-stream"},
		{TrackAudio, "", "", "application/octet-stream"},
		{TrackKind("video"), ".mkv", "", "application/octet-stream"},
	} {
		if got := TrackContentType(tc.kind, tc.ext, tc.charset); got != tc.want {
			t.Errorf("%s %s %q: %q, want %q", tc.kind, tc.ext, tc.charset, got, tc.want)
		}
	}
}

// A track belongs to its source's playback: the video and its sidecars hold
// one slot, while another source is still refused at the limit. Revoking the
// session cuts a running track stream like a video stream.
func TestTrackSharesPlaybackSlotAndIsCutOnRevocation(t *testing.T) {
	resolver, _ := trackFixture(t, "original.ja.srt")
	clock := newFakeClock()
	sessions := &fakeSessions{}
	sessions.active.Store(true)
	handler, err := NewHandler(resolver, Options{MaxConcurrent: 4, WriteTimeout: time.Second, WriteError: limitWriteError, Sessions: sessions,
		SessionCheckInterval: 5 * time.Second, Clock: clock, Limits: Limits{StreamLimit: true, MaxStreamsPerUser: 1}})
	if err != nil {
		t.Fatal(err)
	}
	w := &blockedNetworkWriter{header: make(http.Header), started: make(chan struct{}), expired: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeTrack(w, nativeRequest("GET", "/track"), "source", TrackSubtitle, "track")
	}()
	select {
	case <-w.started:
	case <-time.After(2 * time.Second):
		t.Fatal("track stream did not start")
	}
	video := httptest.NewRecorder()
	handler.ServeSource(video, nativeRequest("GET", "/stream"), "source")
	if video.Code != 200 {
		t.Fatalf("video of the same playback refused: %d", video.Code)
	}
	resolver.sourceID = "other"
	resolver.source.RelativePath = "original.mkv"
	other := httptest.NewRecorder()
	handler.ServeSource(other, nativeRequest("GET", "/stream"), "other")
	if other.Code != http.StatusTooManyRequests {
		t.Fatalf("second playback admitted beside a running track: %d", other.Code)
	}
	clock.waitPending(t, 1)
	sessions.active.Store(false)
	clock.Advance(5 * time.Second)
	expectEnded(t, done, "revoked session kept streaming a track")
	if w.header.Get("Content-Type") != "application/x-subrip; charset=Shift_JIS" {
		t.Fatalf("track stream headers: %v", w.header)
	}
	after := httptest.NewRecorder()
	handler.ServeSource(after, nativeRequest("GET", "/stream"), "other")
	if after.Code != 200 || len(handler.limiter.users) != 0 {
		t.Fatalf("revoked track stream leaked its admission: %d", after.Code)
	}
}

// Track downloads use the same copy paths as sources: on Linux the complete
// and single-range responses go through sendfile with unchanged bytes.
func TestTrackZeroCopyLoopback(t *testing.T) {
	calls := countZeroCopy(t)
	resolver, content := trackFixture(t, "original.ja.srt")
	resolver.track.ETag = ""
	handler := testHandler(t, resolver, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeTrack(w, r.WithContext(access.WithPrincipal(r.Context(), streamPrincipal("u", "s"))), "source", TrackSubtitle, "track")
	}))
	defer server.Close()
	for _, tc := range []struct {
		ranges string
		want   []byte
	}{{"", content}, {"bytes=3-1000", content[3:1001]}} {
		request, _ := http.NewRequest("GET", server.URL, nil)
		if tc.ranges != "" {
			request.Header.Set("Range", tc.ranges)
		}
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || !bytes.Equal(body, tc.want) || response.Header.Get("ETag") != "" || response.Header.Get("Last-Modified") == "" {
			t.Fatalf("range %q: status=%d bytes=%d headers=%v", tc.ranges, response.StatusCode, len(body), response.Header)
		}
	}
	if zeroCopySupported && calls.Load() != 2 || !zeroCopySupported && calls.Load() != 0 {
		t.Fatalf("zero-copy path used %d times", calls.Load())
	}
}
